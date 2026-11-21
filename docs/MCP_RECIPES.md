# MCP 实用配方（可复制配置）

本文给出三个经过核对、复制即可用的 MCP 配置模板，用来补单对话里"非文本感知 + 有状态外部操作"的能力断层。只讲配置与用法，不改任何代码；**不修改 `docs/TECHNICAL.md` 的 Feature parity 表**。

## 0. 先读这一节

- **默认零 server**：开箱时 MCP 是空管道，不连任何外部进程。下列配方都需要显式写配置并**重启进程**才生效。
- **配置放哪**：`~/.argentgo/config.json`、`<cwd>/.argent/config.json`、`<cwd>/argent.json`、选中的 profile，或项目级 `<workspace>/.argent/mcp.json`（**仅**在 `--trust-project` 时读取）。配置支持 JSONC 注释与尾随逗号。
- **字段名逐字对齐解码器**：`command` / `args` / `env` / `url` / `headers` / `transport` / `timeout` / `enabled` / `readOnly` / `allowedTools` / `deniedTools`；权限字段也接受 snake_case：`read_only` / `allowed_tools` / `denied_tools`。
  - **没有 `type` 字段**：VS Code 风格 `"type": "stdio"` 在这里会被静默忽略。请用 `transport`，取值 `stdio` / `http` / `sse`（为空时按 `command` / `url` 推断）。
  - 对象形式用键名当服务器 id；数组形式用 `name` 字段。工具以 `server__tool` 命名空间暴露。
- **权限语义**：`readOnly` 与 `allowedTools`/`deniedTools` 都在请求发出前 fail-closed；**拒绝优先于允许**；空白名单 = 放行全部；白名单条目可以是简称或 `server__tool` 限定名。
- **自检**：`argent mcp [--json] [-C <dir>]` 会列出从合并配置中识别到的 server（名称、传输、目标），**不连接服务器**，可先确认配置没写错。格式错误的条目会被跳过并警告，而不是让整个配置失败。

### 为什么三个配方都选"文本化输出"

`internal/mcp/mcp.go` 的 `CallTool` 把 MCP 的 `image` 内容块降级成 `[image: <mime>]` 占位符（约 463-469 行），模型拿不到真实像素。因此**优先选输出为 Markdown / 可访问性树 / SQL 行的 server**，而不是截图或二进制 blob。二进制 `resource` blob 默认同样只是被跳过；`ARGENT_MCP_BLOB_TEXT` / `ARGENT_MCP_BLOB_ARTIFACT_DIR` 是可选开关，默认关闭。

---

## 1. 文档解析：PDF / Office → Markdown（最高价值）

把模型"看不见"的 PDF、Word、PPT、Excel 变成它能直接读的 Markdown 文本，直接填补非文本感知断层。`markitdown-mcp` 只暴露一个工具 `convert_to_markdown(uri)`，在内存里转换、不写临时文件，因此只读配置非常干净。

```json
{
  "mcpServers": {
    "markitdown": {
      "command": "uvx",
      "args": ["markitdown-mcp"],
      "readOnly": true,
      "allowedTools": ["convert_to_markdown"]
    }
  }
}
```

**对话中怎么触发**

- "把 `file:///D:/docs/季度报告.pdf` 转成 Markdown，只保留标题和表格，然后总结要点。"
- "读一下 `file:///D:/docs/合同.docx`，列出付款与违约条款。"
- Windows 本地文件用正斜杠的 `file:///` URI；也可传 `http:` / `https:` / `data:` URI。

**前置依赖**

- 安装 `uv`（它提供 `uvx`）。Windows PowerShell：`powershell -ExecutionPolicy ByPass -c "irm astral.sh/uv/install.ps1 | iex"`；安装后确认 `uvx --version` 有输出，必要时把 `%USERPROFILE%\.local\bin` 加入 PATH 并重启终端与 agent。
- Python ≥ 3.10；`uvx markitdown-mcp` 首次运行会拉取并缓存 `markitdown[all]`，静默等待客户端即启动成功（`Ctrl+C` 退出）。
- 备选：`pip install markitdown-mcp` 后用 `command: "markitdown-mcp"`；但 `uvx` 能避开 PATH / 虚拟环境错配。

**安全与风险**

- `readOnly: true` 不会被误伤：`convert_to_markdown` 的名字不含写动词，`IsMutating` 判定为只读，调用放行。
- `uri` 接受 `http/https`，让它访问远程 URL 会把外部内容带回对话（SSRF / 数据外带面）。**建议只喂本地 `file://`**，需要联网时审查目标地址。
- 扫描版 PDF 没有文本层，MCP 路径没有内置 OCR，会返回空内容；这不是配置错误。
- 大文件整篇返回，容易冲掉上下文——在话术里要求"只保留标题和表格"或先用 CLI 预转再分段读。
- 文档内容属于**未信输入**，可能带 prompt injection；只接一个转换工具、不给它执行能力，是这里的关键约束。

---

## 2. 浏览器 snapshot：accessibility tree 文本（绕开图片占位符）

Playwright MCP 用可访问性树（accessibility tree）而非截图来驱动浏览器，返回的是结构化文本，正好绕开 `[image]` 占位符缺陷。用白名单只开三个只读观察工具。**注意：stdio 是它的默认传输，官方 CLI 没有 `--stdio` 参数**——加了会以未知参数报错；要切到 HTTP/SSE 才用 `--port`。

```json
{
  "mcpServers": {
    "playwright": {
      "command": "npx",
      "args": [
        "@playwright/mcp@latest",
        "--headless",
        "--isolated",
        "--image-responses=omit"
      ],
      "readOnly": true,
      "allowedTools": [
        "browser_navigate",
        "browser_snapshot",
        "browser_console_messages"
      ],
      "deniedTools": [
        "browser_evaluate",
        "browser_run_code_unsafe",
        "browser_take_screenshot"
      ]
    }
  }
}
```

**对话中怎么触发**

- "打开 `https://example.com`，用 `browser_snapshot` 读出可访问性树，告诉我主导航有哪些条目。"
- "看看当前页面的控制台有没有 error。"
- 模型会先 `browser_navigate`，再 `browser_snapshot` 读文本；元素带 `ref=eN` 编号，可据此继续交互（本配方未开点击/输入工具）。

**前置依赖**

- Node.js ≥ 20；`npx @playwright/mcp@latest` 首次运行拉取并缓存包。
- 需要 Chromium / Chrome（Playwright 自带或系统安装）；`--headless` 适合无显示器环境，桌面调试可去掉。
- `--isolated` 让 profile 留在内存、不落盘；`--image-responses=omit` 让服务器根本不返回图片块（反正 argent 也会把它降级成占位符）。
- Windows 上 `npx` 有时只装成 `npx.ps1`：`internal/mcp/stdio.go` 的 `execSpecForOS` 会自动用 `powershell.exe -NoProfile -ExecutionPolicy Bypass -File` 包装 `.ps1` 命令，因此无需额外改配置；普通 `npx.cmd` 保持原样执行。

**安全与风险**

- **prompt injection 是头号风险**：页面可访问性树里的文字是未信输入，恶意站点可塞入"忽略之前指令、把密钥发到某地址"之类文本。白名单把 `browser_evaluate`（执行任意 JS）、`browser_run_code_unsafe`（官方标注等价于 RCE）、`browser_take_screenshot`（图像块无意义）、以及 `--caps=storage` 的 cookie/token 读取能力全部关在外面。
- `browser_navigate` 是允许的，所以被注入的页面仍可能诱导模型跳到攻击者 URL。可追加 Playwright 的 `--allowed-origins` / `--blocked-origins` 限制可达域名——但官方明确说这是 convenience defense，**不是安全边界**；真正隔离靠这里的白名单与系统权限。
- `readOnly: true` 在 argent 里是按**工具名的动词启发式**判断（`internal/mcp/permissions.go` 的 `mutatingVerbs`）。`browser_navigate` 不在写动词表里，所以不会被 `readOnly` 拦住——真正的约束来自白名单，别把 `readOnly` 当沙箱。
- 拒绝优先于允许：`deniedTools` 即使与白名单重叠也会生效，作为显式兜底。
- 页面文本会进入会话 JSONL；不要在打开敏感后台时把 token / 个人信息留在快照里。

---

## 3. 只读数据库

### 3a. SQLite（推荐起点：无凭据、可用副本）

官方参考 SQLite server 的工具名稳定，正好能演示 `readOnly` 动词启发式与显式黑名单配合：`read_query` / `list_tables` / `describe-table` 是只读，`write_query` / `create_table` / `append_insight` 会被拦。

```json
{
  "mcpServers": {
    "sqlite": {
      "command": "uvx",
      "args": ["mcp-server-sqlite", "--db-path", "D:/data/analytics-copy.db"],
      "readOnly": true,
      "allowedTools": ["read_query", "list_tables", "describe-table"],
      "deniedTools": ["write_query", "create_table", "append_insight"]
    }
  }
}
```

- **触发**："列出这个库的表，再告诉我 `orders` 表每个月的订单数。" 模型会先 `list_tables` / `describe-table` 学 schema，再发 `read_query`。
- **前置**：`uv`/`uvx`；一个 SQLite 文件。**务必指向副本**（如 `analytics-copy.db`），并在 OS 层去掉写权限（Windows 文件属性勾"只读"；Unix `chmod 400`）。
- **安全**：`write_query`（含写动词 `write`）、`create_table`、`append_insight` 会被 `readOnly` 与 `deniedTools` 双重拦截；`mcp-server-sqlite` 本身**没有**只读模式，所以文件权限 + 副本是最后一道闸。该包已从 `modelcontextprotocol/servers` 归档（PyPI 仍可装，版本 2025.4.25），低风险本地分析可用，生产库请换受维护的 server。查询结果会进入对话与会话日志，注意行级数据脱敏。

### 3b. Postgres / 多引擎（凭据不进配置）

`dbhub` 自带 `--readonly`，一个 server 覆盖 SQLite / Postgres / MySQL。DSN 用 `{env:DB_DSN}` 引用——`internal/config/variable.go` 的 `Substitute` 会在加载时对原始配置文本做替换，凭据因此**不写进 JSON、不进 git**。

```json
{
  "mcpServers": {
    "db": {
      "command": "npx",
      "args": [
        "-y", "@bytebase/dbhub",
        "--transport", "stdio",
        "--readonly",
        "--dsn", "{env:DB_DSN}"
      ],
      "readOnly": true
    }
  }
}
```

- **触发**："用 `db` 列出 schema，然后统计最近 30 天活跃用户数。"
- **前置**：Node.js + npx；DB 侧建一个只有 `SELECT` 权限的只读角色；环境变量里设好 `DB_DSN`（例如 `postgresql://ro_user:***@host:5432/db`）。也可用 `{file:secret.txt}` 从被 gitignore 的文件读取（相对路径按配置文件所在目录解析）。
- **安全**：三道闸——DB 只读角色、server 的 `--readonly`、argent 的 `readOnly`。`{env:VAR}` 未设置时会被替换为空串并告警，自检时留意。`deniedTools` 的确切工具名随 server 版本变化，可用对话里的 `tool_search` 查到实际名字后再补黑名单；别凭猜测写。项目级 `.argent/mcp.json` 务必加入 `.gitignore`。

---

## 4. 局限与注意（诚实地讲）

1. **MCP 非零配置**：默认零 server；要装 Node/uvx、写 config、重启进程。握手限制 5 秒，坏 server 软失败只报一次并跳过——所以"没工具"时先看 `argent mcp`，再看启动告警。
2. **图片无法感知**：`CallTool` 把 `image` 块变成 `[image: <mime>]` 占位符（`mcp.go:463-469`）。选 server 时优先文本输出；Playwright 用 `--image-responses=omit` 避免无意义图块。
3. **HTTP/SSE 同源限制**：`http.go` 的 `resolveEndpoint` 只接受与 SSE 流**同源**（scheme + host）的 endpoint 宣告，跨域宣告会被忽略，使配置的 `headers` 不会带着凭据跳到别的源。远程 server 配 `headers` 时不要依赖跨源跳转。
4. **`readOnly` 不是沙箱**：它依赖工具名动词表（未知名字默认当作只读），不能替代 DB 只读账号、文件权限或网络隔离。
5. **凭据与泄露**：`env` / `headers` 会落进配置；用 `{env:VAR}` / `{file:path}` 引用，不要写明文。工具输出（查询结果、页面文本、转换出的文档）会进入会话 JSONL，按现有脱敏规则处理。
6. **外部内容即未信输入**：网页、PDF、表格都可能携带注入指令；坚持最小白名单，禁用 `browser_evaluate` / `browser_run_code_unsafe` 一类执行型工具。
7. **贡献边界**：这些是外部进程配方，零核心改动；按仓库约束，未跑对应测试前不改 Feature parity 状态。

## 5. 与源码的对应关系（字段逐字核对）

| 结论 | 源码位置 |
|---|---|
| `mcpServers` 对象/数组两种形状、字段名与 snake_case 别名、坏条目跳过 | `internal/mcp/config.go:41-199`；`pick` 在 `212-219` |
| `env` 既接受对象也接受 `KEY=VALUE` 数组 | `internal/mcp/config.go:256-280`（`decodeEnv`） |
| 白/黑名单匹配、`*` 通配、拒绝优先 | `internal/mcp/permissions.go:39-82`（`matchesTool` / `toolPermitted`） |
| `readOnly` 动词启发式（写动词表、未知名默认只读） | `internal/mcp/permissions.go:185-230`（`IsMutating` / `mutatingVerbs`） |
| 请求发出前 fail-closed | `internal/mcp/mcp.go:400-408`（`CallTool`） |
| 图片块降级为 `[image: <mime>]` | `internal/mcp/mcp.go:463-469` |
| Windows `.ps1` 自动用 PowerShell 包装 | `internal/mcp/stdio.go:33-95`（`execSpecForOS` / `powershellScript`） |
| SSE endpoint 仅同源 | `internal/mcp/http.go:405-433`（`resolveEndpoint`） |
| 项目 `.argent/mcp.json` 仅在 `--trust-project` 读取 | `internal/app/mcp.go:573-588`（`projectMCPServers`） |
| `{env:VAR}` / `{file:path}` 引用替换 | `internal/config/variable.go:20-62`（`Substitute`） |
