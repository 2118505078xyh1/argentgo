# Pi 技术栈参考（2026-11-24 实测）

> 只读研究资料。本文记录用户当前实际使用的 Pi coding agent（`@earendil-works/pi-coding-agent`
> v1.6.0）的技术栈构成，以及与本项目 `argentgo` 的技术选型对照。
> 不属于本项目源码，结论需按需核实后引用。

---

## 一、总体结论

**Pi 是 TypeScript 项目，不是 Go / Rust / Python。**

它是一个纯 CLI + 终端 UI 形态的 agent harness，通过三种方式对外集成：

1. **JSONL 会话文件**（离线，可回放）
2. **RPC over stdin/stdout**（换行分隔 JSON-RPC）
3. **TypeScript SDK**（进程内创建/控制 agent 会话）

它管理 Go / Rust / Flutter 项目时，是通过 bash 工具调用外部编译器——这与 Pi 自身技术栈无关。

---

## 二、语言与运行时

| 项 | 值 |
|---|---|
| 语言 | **TypeScript** |
| 编译产物 | JavaScript（ESM，`"type": "module"`） |
| 运行时 | Node.js ≥ 22.19（实测本机 v24.19.0） |
| 模块系统 | 原生 ESM |
| 类型声明 | 随包发布 `dist/*.d.ts` |

### Monorepo 子包

全部 `@earendil-works/*`，同版本号发布：

| 包 | 职责 |
|---|---|
| `pi-coding-agent` | CLI 入口、工具层、扩展宿主（本机使用的主包） |
| `pi-agent-core` | agent 循环、上下文组装、事件分发 |
| `pi-ai` | 模型抽象层（provider 协议适配） |
| `pi-tui` | 终端 UI 渲染 |
| `pi-codemode` | QuickJS 沙箱工具 |
| `pi-mcp` | MCP 客户端 |
| `pi-schema` / `pi-telemetry` | Schema 定义与遥测 |

---

## 三、构建与打包

| 项 | 方案 |
|---|---|
| 包管理 | npm（`npm install -g --ignore-scripts`） |
| 类型编译 | `tsc -p tsconfig.build.json` |
| 打包 | 自研脚本 `scripts/build-coding-agent-bundle.mjs` → `dist/bundle/` |
| 单文件二进制 | **Bun** `--compile`（`bun build --compile`），产出 `dist/pi` |
| 依赖锁定 | 官方安装器固定所有依赖；`pi update` 升级 |
| 版本管理 | `install/releases/<version>/` 目录 + `current-version` 指针文件，由 `pi-launcher.js` 分发 |

### 发行形态

- **npm 包**：TS 源码编译的 JS，跨平台
- **Bun 单文件**：macOS / Linux / Windows 各自预编译
- **托管安装**：`~/.pi/agent/install/releases/` 多版本共存，launcher 负责选择

---

## 四、运行时依赖清单

### 主包 `pi-coding-agent`

| 依赖 | 版本 | 用途 |
|---|---|---|
| `chalk` | 6.0.0 | 终端着色 |
| `diff` | 8.0.4 | 差异计算 |
| `highlight.js` | 10.7.3 | 语法高亮 |
| `marked` | 18.0.11 | Markdown 渲染 |
| `marked-cjk-friendly` | 0.1.2 | CJK 行宽友好渲染（`pi-tui`） |
| `get-east-asian-width` | 1.6.0 | 东亚字符宽度计算 |
| `undici` | 8.12.4 | HTTP 客户端 |
| `typebox` | 1.3.27 | Schema 校验 |
| `yaml` | 2.9.0 | YAML 解析 |
| `semver` | 7.8.5 | 版本比较 |
| `quickjs-wasi` | 3.6.2 | **QuickJS WASM 沙箱**（codemode） |
| `jiti` | 2.7.0 | 运行时 TS 加载（扩展） |
| `proper-lockfile` | 4.1.2 | 单实例文件锁 |
| `cross-spawn` | 7.0.6 | 跨平台子进程 |
| `ignore` | 7.0.8 | gitignore 匹配 |
| `brace-expansion` / `minimatch` | — | glob 匹配 |
| `hosted-git-info` | 9.0.3 | 仓库信息 |
| `lovely-mermaid` | 0.3.3 | Mermaid 渲染 |
| `partial-json` | 0.1.7 | 流式 JSON 增量解析（`pi-ai`） |
| `@silvia-odwyer/photon-node` | 0.3.4 | **Rust→WASM 图像处理** |

### `pi-ai`（模型接入层）

原生 SDK 直连，不用 OpenAI 兼容层硬凑：

| 依赖 | 版本 | 对应 |
|---|---|---|
| `@anthropic-ai/sdk` | 0.129.0 | Anthropic Messages |
| `openai` | 7.59.0 | OpenAI Chat / Responses |
| `@google/genai` | 2.32.0 | Google Generative AI |
| `@aws-sdk/client-bedrock-runtime` | 3.1127.0 | AWS Bedrock |
| `http-proxy-agent` / `https-proxy-agent` | 9.1.0 | 走本地代理（Clash） |
| `@smithy/node-http-handler` | 4.12.1 | Bedrock 自定义 handler |

### `pi-tui`

仅 3 个依赖：`marked`、`marked-cjk-friendly`、`get-east-asian-width`。
**无 React / Ink / blessed / blessed-contrib**——终端渲染是纯手写的。

---

## 五、关键技术设计

### 5.1 Codemode：QuickJS WASM 沙箱

Pi 最有特色的设计。模型不再直接调工具，而是写一段 JavaScript 脚本：

```js
const results = await Promise.allSettled([
  tools.read({ path: "a.txt" }),
  tools.read({ path: "b.txt" }),
]);
text(results.map(r => r.value ?? r.reason).join("\n"));
```

沙箱特征（`quickjs-wasi`，WASM 运行时）：

- **无** Node API、文件系统、网络、定时器
- 顶层 `await` / `return` 可用（脚本体是 async function）
- 只能通过 `tools.*` / `models.*` 触达外部世界
- 输出有预算：`max_output_tokens`（默认 10000）、`timeout_ms` 硬超时
- 超限输出的头尾保留，完整内容写临时文件并返回路径
- 只有脚本输出进入模型上下文 → 大幅压缩 token

暴露的全局：`tools` `text` `image` `console` `exit` `store`/`load` `ALL_TOOLS`
`searchTools` `describeTool` `describeNamespace` `models`。

工具描述有 3000 token 预算（`codemode.inlineBudget`），MCP 工具默认走 `deferred` 暴露，不进描述。

### 5.2 扩展系统

扩展是 **TypeScript 模块**，在 Pi 进程内加载：

```ts
export default function activate(api) {
  api.registerTool({ name: "my_tool", ... });
  api.registerCommand("/my-cmd", ...);
  api.registerProvider(...);
  api.on("event", ...);
}
```

能注册：工具、命令、快捷键、provider、事件处理器、渲染器、终端 UI。

**含义：Pi 的扩展不能用 Go / Rust 写**，必须是 TS。

### 5.3 会话树

- 存储：JSONL，每行一个条目，字段含 `type`
- 版本 3（当前）：`id` / `parentId` 构成**树**，同一文件内原地分支
- v1 线性序列（legacy，加载时自动迁移）→ v2 树结构 → v3 `hookMessage` 角色改名 `custom`
- 文件位置：`~/.pi/agent/sessions/--<path>--/<timestamp>_<session-id>.jsonl`
- 压缩（compaction）插入一条摘要条目，替换后续请求中的旧消息；**原条目仍留在树里**

### 5.4 上下文与压缩

- 系统提示 = 基础指令 + 发现到的 context 文件
- 请求携带工具定义与 skill 描述
- **skill 全文按需加载**，不进初始提示
- 压缩策略：`compaction.reserveTokens` / `keepRecentTokens` / `modelOverrides`

### 5.5 信任与权限

- 加载项目设置与资源前先解析**项目信任**
- 启用的工具使用 Pi 进程的操作系统权限
- 扩展在 Pi 进程内执行（无沙箱）

---

## 六、界面形态

| 模式 | 说明 |
|---|---|
| Interactive | 全屏 TUI（自研 `pi-tui`） |
| Print | 跑一个 prompt，输出最终响应 |
| JSON | 以 JSONL 写 agent 事件 |
| RPC | stdin/stdout 上收发 JSONL 命令与事件 |

全部模式共用同一套 agent 与 session 机制。

---

## 七、本机实际配置（2026-11-24）

```jsonc
// ~/.pi/agent/settings.json
{
  "defaultProvider": "magpie",
  "defaultModel": "auto/deepseek-v4.1-flash",
  "defaultTools": ["+grep", "+find", "+ls", "+powershell"],
  "shellPath": "D:\\Git\\bin\\bash.exe",
  "defaultThinkingLevel": "high",
  "compaction": {
    "enabled": true,
    "reserveTokens": 360000,
    "keepRecentTokens": 10000,
    "modelOverrides": { "magpie/auto/deepseek-v4.1-flash": { "reserveTokens": 360000 } }
  }
}
```

```jsonc
// ~/.pi/agent/models.json（节选）
{
  "providers": {
    "magpie": {
      "api": "openai-completions",
      "apiKey": "magpie",
      "baseUrl": "http://127.0.0.1:3425/v1",
      "compat": { "sendSessionAffinityHeaders": true },
      "models": [
        { "id": "auto/deepseek-v4.1-flash", "contextWindow": 116000, "reasoning": true },
        { "id": "workbuddy/deepseek-v4.1-flash", "contextWindow": 1000000 },
        { "id": "workbuddy-ai/hy4flash-free", "contextWindow": 960000 }
      ]
    }
  }
}
```

要点：

- `magpie` provider 指向**本机网关** `127.0.0.1:3425`，Pi 只当 OpenAI 兼容端点调用
- 压缩阈值 360k / 保留 10k，对应 960k 上下文窗口（600k 触发压缩）
- `shellPath` 走 **Git Bash**，不是 PowerShell（`+powershell` 工具是单独加的）

---

## 八、与 argentgo 的技术选型对照

| 维度 | Pi | argentgo |
|---|---|---|
| 语言 | TypeScript | **Go**（1.27.3） |
| 依赖 | ~20 个 npm 包 + 4 个 SDK | **1 个直接依赖**（`mygo`），零传递依赖 |
| 分发 | npm 包 / Bun 单文件 | 单个静态二进制 `argentgo.exe` |
| 运行时 | 需 Node ≥ 22.19（或 Bun） | **无运行时依赖** |
| TUI | 自研 `pi-tui`（纯手写，无 React） | 自研 `internal/tui`（纯手写） |
| 图像处理 | Rust→WASM（photon-node） | Go 原生（`golang.org/x/image`） |
| 沙箱 | QuickJS WASM | Go 进程内 + 路径边界 + 审批链 |
| 扩展语言 | **仅 TypeScript** | Go 插件（`pluginapi.Registry`）+ MCP + skill |
| 工具调用 | 模型直接调 / codemode 脚本批调 | 模型直接调，批执行在 agent 循环内 |
| 会话格式 | JSONL 树（v3） | JSONL 树 |
| 编译速度 | 秒级 | 秒级 |

### 可借鉴点

1. **codemode 的思路**：让模型写脚本批调工具，只有脚本输出进上下文。
   本项目的 agent 循环已支持批执行，但工具结果仍逐条进入上下文——
   可评估是否引入类似"结果聚合"层。
2. **QuickJS 沙箱**：本项目用 Go 进程内隔离 + 审批链，Pi 用 WASM 隔离。
   两者安全模型不同：Go 方案权限更细，WASM 方案隔离更硬。
3. **CJK 友好渲染**：本项目 TUI 若处理中文宽字符，需确认是否有等价的东亚宽度处理。
4. **skill 按需加载**：本项目已有 skills 机制，确认是否同样不进初始提示。

### 差异原因（不是差距）

Pi 与 argentgo 定位不同：Pi 面向**通用开源分发**（npm 安装、跨平台、Bun 单文件），
生态绑定 Node/TS；argentgo 面向**个人长期维护的单文件二进制**，
零依赖、无运行时、可被脚本嵌入。技术选型差异源于分发形态，不是能力强弱。

---

## 九、参考来源

- 本机安装：`~/.pi/agent/install/releases/1.6.0/node_modules/@earendil-works/*`
- 文档：`docs/how-pi-works.md`、`docs/codemode.md`、`docs/session-format.md`、
  `docs/extensions.md`、`docs/sdk.md`、`docs/compaction.md`
- 包元数据：各子包 `package.json`
- 仓库：<https://github.com/earendil-works/pi>（MIT）
