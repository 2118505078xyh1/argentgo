# 内核与插件架构（ADR）

## 背景

`argentgo` 的功能目前分散在多个包里：`internal/commands` 的命令、`internal/hooks`
的钩子、`internal/tui` 的面板与键位、`internal/plugins` 的清单发现各有一套注册方式。
加一条命令常常要同时改注册表、补全表、i18n 目录和执行分支，维护成本高。

目标不是再发明一套插件系统，而是把已有的 `internal/pluginapi.Registry`
提升为统一的扩展契约，把“内核”收敛为 agent 循环与会话，其余能力按功能外置。

## 内核边界

内核只保留五个不可外置的能力：

1. **会话**：JSONL 追加、树形 resume/fork、压缩（`internal/session`）。
2. **事件**：模型流、工具事件、审批事件的有序通道（`internal/agent` + event bridge）。
3. **Provider/协议**：OpenAI、Anthropic、Responses 的线协议（`internal/llm`）。
4. **工具安全链**：路径边界、策略、审批、审计（`internal/security` + `internal/tools`）。
5. **扩展点本身**：`internal/pluginapi` 的注册表与窄接口。

其它能力（斜杠命令、hook、skill、面板、键位、主题、传输）都应当通过扩展点接入。

## 插件分层（按优先级）

1. **MCP server**：外部进程，零核心改动，优先选择。
2. **skill / hook / markdown 命令**：纯文本，随时可改，次选。
3. **进程内 Go 插件**：用 `pluginapi.Registry[T]` 静态注册构造器，编译期决定编入哪些功能；
   顺序用显式 `order` 指定，避免 Go `init()` 顺序漂移。
4. **外部进程 / WASM**：暂不引入，只有在确有隔离需求时再评估。

核心依赖规则不变：`internal/pluginapi` 零依赖；库包之间用参数注入接口，不反向 import。
后端（所有 `internal/` 与 `cmd/`）只允许标准库，可以参考别人代码但不引入第三方依赖；
前端只有 `native/` 例外，使用 MyGo，且不得再引入其它依赖。

## 已完成的第一步

TUI 原生斜杠命令已收敛到 `internal/tui/commands_local.go` 的 `nativeCommands`
注册表：补全名、参数提示、本地化描述和执行体都从这一张表生成，
`model_input.go` 只保留分发。新增命令不再需要同步改三处。

## 后续迁移顺序

每一步都独立可验证，不破坏现有 CLI 行为，不发版：

1. 把 `internal/commands` 的 `/context`、`/review` 迁到统一命令契约，
   宿主注入的 `CommandInfo` 由注册表生成。
2. 把 TUI 的面板开关与帮助文案迁到功能注册表，消除 `help` 长字符串与
   `model_input.go` 按键分支之间的重复。
3. 把 hook/skill 发现统一走 `pluginapi.Registry` 的构造器注册，
   `internal/app` 只做 composition root。
4. 最后评估 provider 与传输层的插件化；它们涉及协议与安全，单独评审。

## 验证

每步都跑仓库快门禁：`gofmt -l .`、`go vet ./...`、`go test -p 4 ./... -count=1`
以及 native 纯逻辑子集。架构依赖由 `internal/pluginapi/dependency_guard.go` 守护。
