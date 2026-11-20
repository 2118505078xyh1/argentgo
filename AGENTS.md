# AGENTS.md

本仓库中 AI agent 的工作指南。

## 这是什么

`argentgo` 是一个个人编码 agent 框架：流式输出模型回复、运行工具、追加到 JSONL 会话，循环往复。它是已冻结的 Rust 项目 `argent`（v0.4.0，已在 GitHub 归档）的 Go 重写版。行为应与该版本保持一致；功能清单见 `docs/TECHNICAL.md`（Feature parity 一节）。

需要对齐的接口：OpenAI chat completions、OpenAI Responses、Anthropic Messages。UI 文案为 zh-CN 与 en-US，嵌入在二进制中。

## 工具链

Go 1.27。宿主系统为 Windows，使用 PowerShell。工具链位于：

```powershell
$env:Path = "$env:USERPROFILE\.tools\go\go\bin;$env:Path"
```

```powershell
go build -o argent.exe ./cmd/argent     # binary
go test ./...                            # all packages
go test ./internal/agent -run TestName   # one test
go test -bench . -benchtime=10x ./...    # benchmark smoke: assertions must pass
go vet ./...                             # vet everything
gofmt -l .                               # must print nothing
```

基准测试也是测试：其断言在 `go test` 下运行，所以即使 CI 从不计时，过时的断言也会让构建失败。只要改动 session/agent/tools 的热路径，就跑一遍 smoke 测试。

桌面前端（`native/`，MyGo 原生 UI）：

```powershell
go build -o argent-native.exe ./native   # the native chat window
```

浏览器前端已移除。`native/` 通过 `--rpc` 驱动引擎。

## 目录布局

```text
cmd/argent          entry point; builds the App and calls cli.RunWith
internal/app        the only package that wires everything together
internal/cli        flags, subcommand dispatch, transports invocation
internal/server     --serve (HTTP/SSE), --rpc, acp
internal/agent      model/tool loop: budget, trimming, dedupe, steering, subagents
internal/llm        wire protocols (chat, anthropic, responses) + retry
internal/provider   provider templates
internal/tools      registry + built-in tools (fs, shell, search, todo, patch)
internal/security   policy, approvals, sandbox modes, audit log
internal/session    JSONL session tree, resume, fork, compaction
internal/config     layered config + profiles
internal/mcp        MCP client (stdio, HTTP/SSE)
internal/hooks      hook documents, SHA-256 trust, dispatch
internal/plugins    plugin manifest discovery/install
internal/lsp        LSP client (diagnostics, definition, hover)
internal/skills     SKILL.md discovery, progressive disclosure
internal/commands   markdown slash commands
internal/memory     AGENTS.md/CLAUDE.md injection
internal/i18n       zh-CN/en-US strings (go:embed)
internal/proc       process spawn/kill trees, timeouts
internal/pty        ConPTY / unix pty
internal/jobs       background job registry
internal/automation cron scheduler + daemon
internal/workflow   scripted multi-step tasks
internal/auth       PKCE / device-code login, token store
internal/update     GitHub release check + self-update
internal/eval       scenario runner (evals/core.json)
internal/tui        full-screen terminal UI
native/             MyGo native desktop chat window (the only GUI frontend)
```

## 依赖规则

- **核心只用标准库。** `internal/{agent,session,llm,tools,security,...}` 包必须保持零依赖。原生桌面壳（`native/`）是唯一显式例外：它需要 `github.com/egoist/mygo` 来做 GPU 渲染的 UI。它通过标准库的 `--rpc` 传输驱动引擎。不要添加其他模块。
- **不使用 cgo。** 所有代码都必须能用 `GOOS=linux go build ./...` 交叉编译。
- Go 工具：`internal/app` 是唯一允许 import 全部包的包。库包可以 import `internal/llm` 和标准库；其余依赖一律通过参数注入接口（`ToolRunner`、`Approver`、`Recorder`、`Chatter`）。
- provider 的协议细节只放在 `internal/llm`，不要把协议怪癖带进 agent 循环。
- `internal/tools` 永不 import `internal/agent`。

## 组织方式与范式

三种范式在这里各司其职；不要强求一种范式去做另一种的工作。

- **模块是包，不是文件。** 模块边界是 `internal/<pkg>` 和上面的依赖规则——把一个大文件拆成多个 `foo_*.go` 只是包内整理，不会改变模块图。按职责（"改一个行为要动几个文件？"）来划分文件边界，而不是按行数。
- **Go 里的 OOP 是组合 + 接口，不是类或继承。** 结构体持有状态，方法操作状态。依赖在调用点注入的小接口（`ToolRunner`、`Approver`、`Recorder`、`Chatter`）；接口定义在消费方，而不是提供方。通过嵌入/组合来复用——这里没有继承，硬造一套继承只会产生僵化的层级。
- **函数式风格用于流式处理和策略注入，不是到处都用。** 一等函数与闭包适合做回调（`onText`、`Emit`、`Compactor`、`RecorderFunc`、惰性 `sync.Once`），也适合在不耦合实现的前提下传入策略。普通的转换就用直白的循环和显式状态：Go 没有丰富的泛型高阶集合标准库，硬套 map/filter/reduce 只会损害可读性。不要为了几个函数式辅助方法就引入依赖。

## 值得为之下测试的不变量

- 每个文件系统工具都把路径解析到工作区内；`..` 和绝对路径逃逸一律拒绝。
- 修改类工具走安全链（策略 → 审批 → 审计）。策略拒绝直接结束运行；审批拒绝变成模型可读的工具错误。
- `--sandbox-mode read-only` 移除修改类工具；`workspace-write`（默认）保留工作区边界；`danger-full-access` 移除该边界。
- 会话是 JSONL，一行一条记录，用 `id`/`parent` 相连。压缩必须保留改动文件、失败与下一步。
- 脱敏发生在任何内容写入审计日志之前。
- 取消 context 会在一轮之内停止运行，工具输出绝不会在一个 UTF-8 字符中间被截断。

## 风格

- `snake_case` 文件名，`PascalCase` 类型名。
- 按职责拆分文件，而不是按行数。一个内聚的 800 行文件，胜过四个你得来回跳转的碎片化 200 行文件。用这个标准检验："改一个行为要打开几个文件？" 如果答案已经是一个，就别动它——不要为了凑数字而拆分。Go 自己的标准库就有几千行的文件；长度不是坏味道，职责混杂才是。
- 测试与源码同目录，表驱动，不联网，不用真实模型。测试名要体现行为：`TestEditRequiresUniqueMatch`。
- 错误：用 `%w` 包装，提供哨兵错误供调用方用 `errors.Is` 匹配。
- 注释解释*为什么*；标识符用英文。
- **Language rule**: apart from code (identifiers, APIs, keywords, command text),
  use Chinese whenever possible for comments and documentation. New comments
  and docs under `docs/`, `AGENTS.md`, `CHANGELOG.md` and inline explanatory
  comments should be written in Chinese (简体中文). Keep code identifiers,
  function/type names and test names in English. User-facing UI strings stay in
  the `i18n` tables (zh-CN / en-US) as before.
- 提交格式：`<area>: <imperative summary>`（例如 `tools: reject path escapes`）。

## 禁止

- 绝不提交构建出来的二进制（`argent.exe`、`argent-native.exe`）或 `node_modules`。原生前端在发布时从 `native/` 源码构建。
- 未运行对应测试前，不要改动 `docs/TECHNICAL.md` 中的 Feature parity 状态。
- 不要为了省 30 行代码而引入依赖。
