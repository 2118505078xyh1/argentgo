# 架构

`argent-go` 是一个分层的 agent harness：所有界面共用同一个循环，装配只发生在一处。

## 请求流

```text
任意表面                          装配              循环              边界
CLI / --serve / --rpc / acp ─┐
全屏 TUI (internal/tui)      ├─> internal/app ─> internal/agent ─> tools / security / session
Web GUI (internal/gui)       ┘        │              │                │
                                      └ 配置/provider  └ Chatter        └ JSONL 会话/审计
```

1. 入口 `cmd/argent` 构造 `cli.App` 并调用 `cli.RunWith`：解析标志、分发子命令或进入
   `runMain`。`internal/cli` 不认识 agent，只通过注入的 `RunnerFactory` 拿到 runner。
2. `internal/app` 是唯一允许 import 全部包的包。它按 `Resolve` 合并配置与 provider 模板，
   打开会话存储，为每次运行新建工具注册表、安全链和 `agent.Runner`，然后交给 loop。
3. `internal/agent` 是 provider 无关的循环：裁剪上下文、委派 `Chatter`、读取工具规格、
   执行批次、检查预算、去重、注入 steering、写 `Recorder`、发 `Event`。它只认接口。

## 注入边界

| 接口 | 定义处 | 职责 |
|---|---|---|
| `Chatter` | `internal/llm` | `Chat(ctx, model, messages, tools, onText)`；三种协议客户端实现它 |
| `ToolRunner` | `internal/agent` | `Specs()` / `Run(ctx, name, args)`；注册表与假实现都可实现 |
| `Approver` | `internal/security` | `Ask(ctx, name, args)`；交互审批、`never`、`auto` 三种实现 |
| `Recorder` | `internal/agent` | `Append(kind, payload)`；由会话的 `Append` 适配 |

`internal/app.Execute` 是接线点：`Chatter` 来自 `newChatter`（含 fallback），
`ToolRunner` 是带 guard 的注册表（内置工具 + MCP + `use_skill`），
`Approver` 由审批模式选择，`Recorder` 绑定到当前会话。事件通过 `Emit` 回传给各表面。
换掉任一接口即可在测试里使用假实现，不需要网络或真实模型。

## 三种无头传输

`internal/server` 承载同一 agent 的三个入口，全部依赖注入的 `server.Runner`：

- `--serve`：`127.0.0.1:7777`（可用 `--serve-address` / `--serve-port` 改），
  Bearer token 强制；`/v1/prompt`、`/v1/events`（SSE）、`/v1/abort`、`/v1/health` 等。
- `--rpc`：stdin/stdout 上的换行分隔 JSON-RPC，`Prompt` / `Abort` / 会话方法；诊断走 stderr。
- `acp`：stdio 上的 Agent Client Protocol（`initialize`、`session/new`、`session/prompt` 等）。

三者在改循环时必须保持事件、审批与 steering 语义一致。

## 终端与图形界面

- **TUI**（`internal/tui`）：自绘 ANSI 全屏，含 streaming transcript、markdown-lite、
  输入编辑器、审批弹窗、主题与 locale；不经 WebView，可在 SSH 上运行。入口 `argent tui`。
- **GUI**（`internal/gui` + `ui/`）：Go 后端在 `127.0.0.1` 随机端口上提供
  `GET /`（`go:embed` 的 `ui/dist`）、`POST /rpc/{command}`、`GET /events`（SSE）、
  `/healthz`；保留旧 Tauri 的命令名与事件负载，但改走 HTTP，每个标签页一个注入的 runner。
  前端是 SolidJS + Vite（bun 构建），`internal/gui/dist/index.html` 是占位文件，
  让 `go:embed` 在首次构建前也能编译。
- **启动器**（`internal/launcher`）：计划用 Edge/Chrome `--app` 打开上面带 token 的 URL，
  失败回退系统浏览器。**仍在开发中**：GUI 后端已落地，启动器与前端传输桥接未完成，
  `argent gui` 目前解析后明确报错退出。

## 配置与依赖规则

配置优先级（低 → 高）：编译默认值 → `~/.argent-go/config.json` →
`<cwd>/.argent/config.json` → `<cwd>/argent.json` → profile → `ARGENT_*` → CLI 标志。
`-c/--config` 落在配置文件层（点分键嵌套），低于 env 与标志。

- **stdlib only**：无第三方模块、无 `go get`，`go.mod` 无 `require`。
- **无 cgo**：`GOOS=linux go build ./...` 必须能交叉编译。
- 只有 `internal/app` 可以 import 一切；库包可 import `internal/llm` 与标准库，
  其余依赖一律通过上面的接口注入。
- 协议细节只在 `internal/llm`；`internal/tools` 永不 import `internal/agent`。
