# argent-go

个人使用的 Agent Runtime：流式输出模型回复、调用工具、追加到 JSONL 会话、循环往复。
它是已冻结的 Rust 版 `argent`（v0.4.0）的 Go 重写，行为对齐该版本；
逐项状态见 [docs/PARITY.md](docs/PARITY.md)。

- **纯 Go**：标准库实现，**无第三方依赖**（`go.mod` 没有 `require`），**无 cgo**，
  单一可执行文件；设置 `GOOS=linux go build ./...` 即可交叉编译。
- **多界面**：一次性 CLI、全屏 TUI、内嵌 Web GUI，以及三种无头传输。
- **双语**：zh-CN / en-US 文案嵌在二进制里，没有外部 locale 文件。

体积是实测数字，不是对标目标：无头运行工作集约 **10 MB**，Go 二进制约 **10.6 MB**。
不要据此推断 GUI 的内存占用——GUI 路径尚未做端到端测量。

## 快速开始

```powershell
go build -o argent.exe ./cmd/argent     # 构建单文件二进制

argent "把 src 里的 unwrap 都找出来"      # 一次性提示，打印回答
argent --mode json "列出 crates 目录"     # 严格 JSONL：事件 + 结果
argent tui                               # 全屏终端 UI（SSH 也能跑）
```

其他入口：

```powershell
argent --serve --serve-token <token>     # HTTP + SSE；无 token 拒绝启动
echo '{"id":1,"method":"get_session_stats"}' | argent --rpc   # stdio 上的换行分隔 JSON-RPC
argent acp                               # Agent Client Protocol 服务端
argent gui                               # 桌面应用窗口（Go HTTP + 内嵌 SolidJS + WebView2）
```

`argent gui` 起本地 HTTP 服务，注入一次性 token，然后用 Edge/Chrome 的应用模式打开
无地址栏的独立窗口（没有 Edge/Chrome 时退回默认浏览器标签页）。前后端只走
`POST /rpc/<command>` + `GET /events` SSE，不依赖 Tauri/Rust。详见
[docs/FRONTEND.md](docs/FRONTEND.md)。

## 配置

配置分层，低 → 高：编译默认值 → `~/.argent-go/config.json` → `<cwd>/.argent/config.json`
→ `<cwd>/argent.json` → profile → `ARGENT_*` 环境变量 → CLI 标志。
`-c/--config key=value` 在配置文件层生效（点分键可嵌套），低于环境变量与标志。
profile 由 `ARGENT_PROFILE` 或配置里的 `profile` 选中，位于配置文件与环境变量之间。

| 设置 | 标志 | 环境变量 | 默认值 |
|---|---|---|---|
| 模型 | `-m`, `--model` | `ARGENT_MODEL` | `gpt-4o-mini` |
| provider 模板 | `--provider` | `ARGENT_PROVIDER` | 按协议推断 |
| 协议 | `--protocol` | `ARGENT_PROTOCOL` | `openai` |
| base URL | `--base-url` | `ARGENT_BASE_URL`, `OPENAI_BASE_URL` | `https://api.openai.com/v1` |
| API key | `--api-key` | `ARGENT_API_KEY`, `OPENAI_API_KEY`，或模板指定的 key 变量 | 空 |
| profile | `--profile` | `ARGENT_PROFILE` | 空 |
| locale | `--locale` | `ARGENT_LOCALE` | `en-US` |
| 沙箱模式 | `--sandbox-mode` | `ARGENT_SANDBOX_MODE` | `workspace-write` |
| 审批模式 | `--approval` | `ARGENT_APPROVAL` | `ask` |
| 存储目录 | — | `ARGENT_STORAGE`, `ARGENT_STORAGE_DIR`, `ARGENT_HOME` | `~/.argent-go` |
| 回合上限 | `--max-turns` | `ARGENT_MAX_TURNS` | `12` |
| 自动批准 | `--yolo` | `ARGENT_YOLO=1` | 关 |
| 服务令牌 | `--serve-token` | `ARGENT_SERVE_TOKEN`, `ARGENT_TOKEN` | 空（`--serve` 必填） |

内置 provider 模板：`anthropic`、`openai`、`deepseek`、`moonshot`、`zhipu`、`zai`、
`bigmodel`、`qwen`、`minimax`、`openrouter`、`ollama`、`lmstudio`、`local`，
另有别名（`claude`、`kimi`、`glm`、`grok` 等）。模板可在配置文件 `providers` 块覆盖或新增。
协议：Anthropic Messages、OpenAI chat completions、OpenAI Responses，均支持流式与工具调用。

## 工作区安全模型

这不是操作系统级隔离。要强隔离，把 argent 放进容器运行。

- 每个文件工具都把路径解析到工作区内；`..` 和绝对路径逃逸一律拒绝。策略同时检查词法路径
  与 canonical 目标（含不存在文件的尾部），符号链接不能绕过白名单。
- 所有修改类工具走同一条安全链：**策略（硬门禁）→ 审批 → 脱敏审计日志**。
  策略拒绝直接结束运行；审批拒绝变成模型可读的工具错误。`--yolo` 等价于自动批准。
- 沙箱模式：`read-only` 在注册表层移除修改类工具；`workspace-write`（默认）保留工作区
  边界；`danger-full-access` 移除边界。`--add-dir` 扩展可写根目录，目前仍在补齐。
- 会话是 JSONL，每行一条，用 `id`/`parent` 连成树；压缩保留改动文件、失败与下一步。
- 审计写在 `<storage>/audit/<day>.jsonl`，密钥、token 等敏感值在落盘前脱敏。

## 模块地图

```
cmd/argent          入口：构造 App，调用 cli.RunWith
internal/app        唯一允许 import 全部包的装配层；解析配置/构造 runner
internal/cli        标志、子命令分发、传输入口
internal/server     --serve (HTTP/SSE)、--rpc、acp
internal/agent      模型/工具循环：预算、裁剪、去重、steering、子代理
internal/llm        三种线协议 + 重试；协议细节只留在这里
internal/provider   provider 模板
internal/tools      注册表与内置工具（fs/shell/search/todo/patch）
internal/security   策略、审批、沙箱、脱敏审计
internal/session    JSONL 会话树、resume、fork、压缩
internal/config     分层配置 + profiles
internal/mcp / hooks / plugins / lsp / skills / commands / memory / i18n
                    MCP 客户端、hook 信任、插件清单、LSP、SKILL.md、斜杠命令、项目记忆、双语
internal/proc / pty / jobs / automation / workflow
                    进程树、伪终端、后台任务、调度器、脚本化工作流
internal/auth / update / eval
                    PKCE/设备码登录、自更新、场景评测
internal/tui        全屏终端 UI
internal/gui        Web 后端：HTTP + SSE + go:embed 的 ui/dist
ui/                 SolidJS + Vite 前端（bun 构建）
```

依赖规则：`internal/app` 之外不得 import 全部包；库包只能依赖 `internal/llm` 与标准库，
其余依赖用 `Chatter`、`ToolRunner`、`Approver`、`Recorder` 接口注入；
`internal/tools` 永不 import `internal/agent`。详见 [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)。

## 测试

```powershell
$env:Path = "$env:USERPROFILE\.tools\go\go\bin;$env:Path"   # Go 不在 PATH 时
go build -o argent.exe ./cmd/argent
go test ./...
go test ./internal/agent -run TestName
go vet ./...
gofmt -l .                       # 必须无输出

cd ui; bun install; bun run typecheck; bun run test; bun run build
powershell -File scripts/build-ui.ps1    # 构建前端并复制到 internal/gui/dist
```

## Windows 一键发布与安装

发布只面向 Windows/amd64。先把 `internal/cli/cli.go` 的 `Version` 改好、在
`CHANGELOG.md` 加 `## v<Version>` 小节，提交并保持工作区干净，然后：

```powershell
powershell -ExecutionPolicy Bypass -File scripts/publish-win.ps1 -DryRun   # 只校验与本地构建，不推送
powershell -ExecutionPolicy Bypass -File scripts/publish-win.ps1           # 推 main + tag，等 Release URL
```

脚本会拒绝非 `main` 分支、脏工作区、Version/tag/CHANGELOG 不一致、已有 tag 和隐私扫描
命中；推送后必须等到 GitHub Release 真的出现且带 `argent_<version>_windows_amd64.zip`
与 `checksums.txt`，否则报错——tag 本身不是 Release。Release notes 取自 CHANGELOG
对应小节。

本地安装 / 卸载（默认 `%LOCALAPPDATA%\argent-go\bin`）：

```powershell
powershell -ExecutionPolicy Bypass -File scripts/install-win.ps1
powershell -ExecutionPolicy Bypass -File scripts/uninstall-win.ps1
```

详见 [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) 的「发布流程」一节。

## 明确未移植

以下能力**没有**移植，且不是“正在做”：

- 原生 C-ABI 插件加载器（Go 改用进程 / MCP 插件）
- tree-sitter AST 与 `repo_map`（改用 LSP 符号）
- Rhai 脚本插件
- 桌面控制工具（`enable-desktop`）
- 遥测（telemetry）
- 插件 marketplace

仍在进行中（会解析但明确报错退出，不会静默忽略）：`--worktree`、`--background`、
`--enable-browser`、`--enable-desktop`、`--enable-service`、`--workflow-mode`、
`--workflow-resume`、`--workflow-journal`、`--gui`；`theme`/`no-color`、`--add-dir`、
`--resume`、`--output-schema` 的行为仍待接线。逐项状态见 [docs/PARITY.md](docs/PARITY.md)。
