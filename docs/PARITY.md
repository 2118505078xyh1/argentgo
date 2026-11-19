# 功能对等清单（Rust → Go）

基准：`argent` v0.4.0（已冻结）。清单由源码生成，不是凭记忆：

- flag：`crates/cli/src/args.rs` 的 57 个字段
- 子命令：`crates/cli/src/commands.rs` 的 27 个分支
- 工具：`crates/tools/src/*.rs` 的 `fn name()` 共 21 个

状态：`done` 已迁移并通过测试 · `wip` 有代码未接线 · `todo` 未开始 · `skip` 明确不迁（附理由）

## 工具（21）

| Rust | Go | 状态 | 说明 |
|---|---|---|---|
| read write edit bash ls grep find | 同名 | done | |
| apply_patch | multi_edit | done | 语义等价：多段替换一次写入 |
| todo | todo | done | |
| glob（Rust 走 find/grep） | glob | done | |
| web_search | web_fetch | wip | fetch 已做；search 需接搜索 API |
| current_time | — | todo | 一行工具，集成时补 |
| delegate | agent 的 `task` | wip | wave-2 agent 切片 |
| memory | memory | wip | wave-2 context 切片 |
| skill | `use_skill` | wip | wave-2 context 切片 |
| tool_search | — | todo | 工具提升（隐藏工具目录） |
| submit_plan | — | todo | plan 模式 |
| code | — | skip | 等价于 bash + 临时文件，重复 |
| document | — | skip | office 预览只服务桌面端 |
| repo_map | LSP documentSymbol | todo | 不用 tree-sitter，走 LSP |
| probe | — | skip | 实验性自检工具，无用户面 |
| ask | 审批链路 | n/a | Go 里审批是通道，不是工具 |

## CLI flag（57）

done（20）：`prompt model mode json_lines cwd base_url api_key yolo max_turns approval locale config_overrides rpc serve serve_port serve_token serve_prompt acp help version`

todo（30）：`provider profile list_profiles workflow_mode list_sessions resume continue_session plugins max_tokens max_cost max_seconds price_input price_output fallback_model secondary_model add_dirs output_schema background worktree list_models no_color gui tokens trust_project enable_browser enable_desktop enable_service sandbox thinking no_auto_compact theme`

skip（7）：
- `doctor` `workflow` `workflow_resume` `workflow_journal` → 改为子命令（Rust 里是双入口，Go 只留子命令）
- `serve_address` → 用 `--serve-port` + 配置项
- `sandbox` → 与 `--sandbox-mode` 重复

## 子命令（27）

done（4）：`sessions doctor login eval`

todo（18）：`logout auth models mcp skills commands hooks workflow telemetry audit usage policy update completion replay attach logs follow stop board bug`

skip（5）：`attach follow stop logs` 合并进 `sessions` 子命令族；`board` 为实验看板

## 子系统

| 子系统 | Go 包 | 状态 |
|---|---|---|
| 3 套协议（Anthropic / chat / Responses） | `internal/llm` | done |
| provider 模板 | `internal/provider` | done |
| agent 循环（预算/裁剪/去重/steering/子代理） | `internal/agent` | wip |
| 工具注册表 + guard | `internal/tools` | done |
| 策略 / 审批 / 审计 / 沙箱 | `internal/security` | done |
| 会话树 / fork / 压缩 | `internal/session` | done |
| 分层配置 / profiles | `internal/config` | done |
| MCP（stdio / HTTP-SSE） | `internal/mcp` | done |
| hooks（11 事件 + 信任） | `internal/hooks` | done |
| 插件清单发现 / 安装 | `internal/plugins` | wip |
| LSP | `internal/lsp` | wip |
| skills / commands / memory / i18n | 同名 | wip |
| TUI | `internal/tui` | wip |
| CLI + `--serve` / `--rpc` / `acp` | `internal/cli` `internal/server` | wip |
| pty / jobs / 进程控制 | `internal/proc` `pty` `jobs` | wip |
| automation / workflow | 同名 | wip |
| auth（PKCE / 设备码）/ update | 同名 | wip |
| eval 场景 | `internal/eval` | wip |
| 原生 C-ABI 插件加载器 | 进程插件 | skip（Go 无该生态） |
| tree-sitter AST | LSP 符号 | skip（cgo 会破坏静态交叉编译） |
| Rhai 脚本插件 | goja（待定） | todo |
| device（桌面控制） | — | skip |
| telemetry | — | skip（个人项目无收益） |
| 桌面壳（Tauri） | wave 3 | todo |

## 验收规则（不靠文档靠测试）

1. `internal/cli/parity_test.go`：断言 flag 表、子命令表与上面的 done 集合一致；少一个即红。
2. `evals/core.json` 移植后，同一批场景在 Go 上跑通，通过率必须与 Rust 版一致。
3. 每轮集成先 `go vet ./...` + `go test ./...`，再对表勾状态。

这份清单是「还差什么」的唯一来源；代码里的 `TODO` 不算。
