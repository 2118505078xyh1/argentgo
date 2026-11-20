# 架构

`argentgo` 是一个分层的 agent harness：所有界面共用同一个循环，装配只发生在一处。

## 请求流

```text
任意表面                          装配              循环              边界
CLI / --serve / --rpc / acp ─┐
全屏 TUI (internal/tui)      ├─> internal/app ─> internal/agent ─> tools / security / session
原生 GUI (native/ → --rpc)  ┘        │              │                │
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
| `SkillWriter` / `SkillLister` | `internal/agent` | 可选的技能自写/列举接缝；由 `internal/skills` 适配器实现 |

`internal/app.Execute` 是接线点：`Chatter` 来自 `newChatter`（含 fallback），
`ToolRunner` 是带 guard 的注册表（内置工具 + MCP + `use_skill`），
`Approver` 由审批模式选择，`Recorder` 绑定到当前会话。配置里的权限规则经
`policyFor(res)` 进入 `security.Policy`，hook 文档在 run 前后派发。事件通过
`Emit` 回传给各表面；换掉任一接口即可在测试里使用假实现，不需要网络或真实模型。

## 三种无头传输

`internal/server` 承载同一 agent 的三个入口，全部依赖注入的 `server.Runner`：

- `--serve`：`127.0.0.1:7777`（可用 `--serve-address` / `--serve-port` 改），
  Bearer token 强制；`/v1/prompt`、`/v1/events`（SSE）、`/v1/abort`、`/v1/health` 等。
- `--rpc`：stdin/stdout 上的换行分隔 JSON-RPC，`Prompt` / `Abort` / 会话方法；诊断走 stderr。
- `acp`：stdio 上的 Agent Client Protocol（`initialize`、`session/new`、`session/prompt` 等）。

三者在改循环时必须保持事件、审批与 steering 语义一致。

## 终端与图形界面

- **TUI**（`internal/tui`）：自绘 ANSI 全屏，含 streaming transcript、markdown-lite、
  输入编辑器、审批弹窗、主题与 locale；Ctrl+T/B/P 分别打开任务、工作区、计划面板；
  不经 WebView，可在 SSH 上运行。入口 `argent tui`。
- **GUI**（`native/`）：MyGo 原生聊天窗口，是唯一的桌面前端。它用 `ARGENT_EXE`
  拉起 `argent.exe --rpc`，通过 stdio JSON-RPC 驱动引擎：`prompt` + `prompt/delta`
  流式通知、`abort`、`session.list`、`session.load`。窗口由 Go 的 `ui` 包自绘，
  无 WebView、无浏览器、无 HTTP 服务。侧栏、消息列表、工具卡片、审批、
  Markdown 流式渲染都在原生控件里完成。
- **入口**（`cmd/argent/gui.go`）：`argent gui` 解析标志、`app.Resolve` 装配引擎，
  然后启动同目录的 `argent-native.exe`（GUI 子系统，无黑色控制台）。桌面只走原生窗口。

## 配置与依赖规则

配置优先级（低 → 高）：编译默认值 → `~/.argentgo/config.json` →
`<cwd>/.argent/config.json` → `<cwd>/argent.json` → profile → `ARGENT_*` → CLI 标志。
`-c/--config` 落在配置文件层（点分键嵌套），低于 env 与标志。

- **stdlib only**：无第三方模块、无 `go get`，`go.mod` 无 `require`。
- **无 cgo**：`GOOS=linux go build ./...` 必须能交叉编译。
- 只有 `internal/app` 可以 import 一切；库包可 import `internal/llm` 与标准库，
  其余依赖一律通过上面的接口注入。
- 协议细节只在 `internal/llm`；`internal/tools` 永不 import `internal/agent`。

## 发布流程（Windows）

- 唯一事实来源是 `internal/cli/cli.go` 的 `Version`；`scripts/build-release.ps1` 只校验
  `-Version`/`-Tag`，从不覆盖它，所以二进制自报版本与 tag 不会分叉。
- `scripts/check-release-consistency.ps1` 校验 Version / tag / CHANGELOG 一致，
  `scripts/privacy-scan.ps1` 扫描已跟踪文件里的高置信度密钥标记。
- `scripts/publish-win.ps1` 是单命令发布入口：校验 clean `main` 与版本一致性 →
  构建 Windows/amd64 单一 zip + checksums → 推 main 与 tag → 等 GitHub Actions
  真正发布 Release 并打印 URL；10 分钟内没有带资产的 Release 就报错。
  `-DryRun` 只做校验与本地构建，不推送。
- `.github/workflows/release.yml` 在 tag 推送后于 ubuntu-latest 上用 pwsh 构建、
  校验 checksums、从 CHANGELOG 抽取 release notes，再用 `gh release create
  --verify-tag` 上传 zip 与 checksums，并回查资产确实存在。tag 本身不是 Release。
- 本地安装/卸载用 `scripts/install-win.ps1` / `scripts/uninstall-win.ps1`
  （默认 `%LOCALAPPDATA%\argentgo\bin`），不会动 `~/.argentgo` 里的会话和配置。

## See also

- [Architecture diagrams](ARCHITECTURE_DIAGRAMS.md) — Mermaid views of the layers,
  request flow, session tree, LSP activation and distribution pipeline.
- [API reference](API_REFERENCE.md) — CLI subcommands and flags, RPC/HTTP-SSE
  transports, built-in tools, MCP configuration and config layering.
