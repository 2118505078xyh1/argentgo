# 功能对等清单（Rust → Go）

基准：`argent` v0.4.0（已冻结）。清单由源码生成，不是凭记忆：

- flag：`crates/cli/src/args.rs` 的 57 个字段
- 子命令：`crates/cli/src/commands.rs` 的 27 个分支
- 工具：`crates/tools/src/*.rs` 的 `fn name()` 共 21 个

状态：`done` 已迁移并通过测试 · `in progress` 有代码/后端已落地但仍待接线 ·
`parse-then-error` 解析后明确报错退出 · `todo` 未开始 · `skip` 明确不迁（附理由）

## 工具（21）

| Rust | Go | 状态 | 说明 |
|---|---|---|---|
| read write edit bash ls grep find | 同名 | done | |
| apply_patch | multi_edit | done | 语义等价：多段替换一次写入 |
| todo | todo | done | |
| glob（Rust 走 find/grep） | glob | done | |
| web_search | `web_search` | done | 可配置 `ARGENT_WEB_SEARCH_URL`；未配置时明确建议 `web_fetch` |
| current_time | `current_time` | done | UTC/本地/IANA 时区，RFC3339/date/time/unix |
| delegate | agent 的 `task` | done | 子代理/嵌套循环已落地 |
| memory | 项目记忆注入 | done | 以 system prompt 注入实现，非独立可调用工具 |
| skill | `use_skill` | done | SKILL.md 发现 + 渐进披露已接线 |
| tool_search | `tool_search` | done | 按名称/描述检索注册工具 |
| submit_plan | `submit_plan` | done | 会话目录写入 plan.json，不执行步骤 |
| code | — | skip | 等价于 bash + 临时文件，重复 |
| document | — | skip | office 预览只服务桌面端 |
| repo_map | LSP documentSymbol | done | 不用 tree-sitter，走 LSP 符号 |
| probe | — | skip | 实验性自检工具，无用户面 |
| ask | 审批链路 | n/a | Go 里审批是通道，不是工具 |

## CLI flag（57）

done（已落地并生效）：

`prompt model mode json_lines cwd base_url api_key yolo max_turns approval locale
config_overrides rpc serve serve_port serve_token acp help version provider profile
list_profiles list_sessions list_models sandbox max_tokens max_cost max_seconds
price_input price_output fallback_model tokens trust_project thinking
no_auto_compact plugins`

parse-then-error（解析成功但明确报错退出，exit 1，不静默忽略）：

`worktree background enable_browser enable_desktop enable_service workflow_mode
workflow_resume workflow_journal gui`

- `--worktree`：自行创建 worktree 后用 `-C` 指定
- `--background`：用外部进程管理器托管
- `--enable-browser` / `--enable-desktop` / `--enable-service`：对应工具未移植
- `--workflow-mode` / `--workflow-resume` / `--workflow-journal`：journal 未移植；
  可自行调用 `internal/workflow`
- `--gui`：GUI 后端已落地，启动器与 UI 桥接仍在进行中

in progress（能解析，行为仍待接线）：

`theme no_color add_dir resume continue_session output_schema secondary_model`

- `theme` / `no_color`：TUI 主题切换仍在进行中
- `add_dir`：路径解析仍限制在 `--cwd` 内，扩展可写根目录未接线
- `resume`（含 `--continue` / `--continue-session` 别名）：会话恢复仍在补齐
- `output_schema`：schema 已加载进 system prompt，失败重试/校验仍在补齐
- `secondary_model`：字段已携带，子代理模型选择未接线

skip（按设计不做 flag）：

- `serve_prompt` → 由 `--serve` 的运行参数承载，无独立 flag
- `doctor` `workflow` `workflow_resume` `workflow_journal` → 改为子命令（Rust 是双入口，Go 只留子命令）
- `serve_address` → 用 `--serve-port` + 配置项
- `sandbox` → `--sandbox-mode` 的别名（保留兼容，非独立 flag）

## 子命令（27）

done（已分发并通过测试）：

`sessions doctor login eval logout auth models mcp skills commands hooks audit usage
policy update completion replay bug config tui`

（Go 另有 `help` / `version` 两个入口，等价于 `-h` / `-V`。）

in progress（尚未在 Go 中落地）：

`workflow telemetry attach logs follow stop board`

- `workflow` / `telemetry`：子命令仍在进行中（后端 `internal/workflow` 已落地，telemetry 明确不迁）
- `attach` `logs` `follow` `stop`：计划合并进 `sessions` 子命令族，尚未接线
- `board`：实验看板，仍在进行中（原计划当作 `skip`，当前按进行中跟踪）

## 子系统

| 子系统 | Go 包 | 状态 |
|---|---|---|
| 3 套协议（Anthropic / chat / Responses） | `internal/llm` | done |
| provider 模板 | `internal/provider` | done |
| agent 循环（预算/裁剪/去重/steering/子代理） | `internal/agent` | done |
| 工具注册表 + guard | `internal/tools` | done |
| 策略 / 审批 / 审计 / 沙箱 | `internal/security` | done |
| 会话树 / fork / 压缩 | `internal/session` | done |
| 分层配置 / profiles | `internal/config` | done |
| MCP（stdio / HTTP-SSE） | `internal/mcp` | done |
| hooks（11 事件 + 信任） | `internal/hooks` | done |
| 插件清单发现 / 安装 | `internal/plugins` | done（marketplace 不迁） |
| LSP | `internal/lsp` | done |
| skills / commands / memory / i18n | 同名 | done |
| TUI | `internal/tui` | done |
| CLI + `--serve` / `--rpc` / `acp` | `internal/cli` `internal/server` | done |
| pty / jobs / 进程控制 | `internal/proc` `pty` `jobs` | done |
| automation / workflow | 同名 | done |
| auth（PKCE / 设备码）/ update | 同名 | done |
| eval 场景 | `internal/eval` | done |
| 装配层 | `internal/app` | done |
| 原生 C-ABI 插件加载器 | 进程插件 | skip（Go 无该生态） |
| tree-sitter AST | LSP 符号 | skip（cgo 会破坏静态交叉编译） |
| Rhai 脚本插件 | — | skip（明确不迁） |
| device（桌面控制） | — | skip |
| telemetry | — | skip（个人项目无收益；子命令仍 in progress） |
| 安全策略规则（用户配置） | `internal/app/policy_config.go` | done：配置规则进入 `security.Policy` |
| hooks 运行时派发 | `internal/app/hooks.go` | done：7 事件接入真实 run，exit-2 阻断工具 |
| MCP per-server 权限 | `internal/mcp` | done：enabled/readOnly/allowedTools/deniedTools |
| TUI tasks/workspace/plan 面板 | `internal/tui` | done：Ctrl+T/B/P |
| 桌面壳（原生） | `native/`（MyGo） | done：纯 Go 原生 UI，经 `--rpc` 驱动引擎，无 WebView |

## 验收规则（不靠文档靠测试）

1. `internal/cli/parity_test.go`：断言 flag 表、子命令表与上面的 done 集合一致；少一个即红。
2. `evals/core.json` 移植后，同一批场景在 Go 上跑通，通过率必须与 Rust 版一致。
3. 每轮集成先 `go vet ./...` + `go test ./...`，再对表勾状态。

这份清单是「还差什么」的唯一来源；代码里的 `TODO` 不算。
