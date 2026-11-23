# 产品打磨路线图（2026-11-23）

> 来源：5 个用户研究/竞品 UX 代理并行审计（TUI 旅程、首次体验、原生 GUI、关键时刻、竞品交互范式），源码行号证据见各代理报告。本文是**产品决策**，不是功能罗列——按「首次留存 / 日常顺手 / 信任感」三条标准筛选，只做命中项。
>
> 产品形态（用户 2026-11-23 定调）：用户只选模型 + 思考强度，然后对话；状态栏默认可见上下文/缓存/模型/思考强度/速度；其余内部选择题项目自主决定。

## 核心判断

底层能力不缺，缺的是**「做了但没接出来」**与**「关键事件到不了用户眼前」**。多份报告独立复现了同一组实锤：

| 实锤问题 | 证据 | 报告共识 |
|---|---|---|
| 审批弹窗无真实 diff（`ApproveDiff` 生产零调用） | `internal/tui/approval.go:168` 仅测试调用 | 3 份 |
| undo 绕过安全链（`CheckRestore` 生产零调用） | `internal/security/chain.go:226` 仅测试调用 | 2 份 |
| 友好建议死代码（`authExpiryAdvice`/`confirmAdvice`/`contextBudgetAdvice` 零调用） | `internal/cli/*advice.go` 仅测试 | 3 份 |
| 首屏空白 + 快捷键零可发现（hintLine 恒空，help 藏在 /help 里） | `internal/tui/render.go:1234-1241` | 2 份 |
| 错误/压缩事件被 TUI 桥接器吞掉 | `internal/app/tui_event_wire.go:44-46,121-132` | 2 份 |
| 无 API key 时先打空 key 请求，401 误导成「填错了」 | `internal/app/app_resolve.go:254` | 1 份 |
| 配置路径双轨（CLI 写 storage 目录，引擎读 home 目录） | `internal/cli/cli.go:699` vs `internal/config/config.go` | 1 份 |
| GUI 顶栏硬编码 `gpt-4o-mini`/`~/my-project`，假 demo 冒充真实 | `native/main.go:648,844,849` | 1 份 |

## 分档决策

### P0 — 立刻做（信任 + 留存，命中三条标准）

| # | 改动 | 产品理由 | 成本 | 落地 |
|---|---|---|---|---|
| P0-1 | **审批接真实 diff**：把安全链已算出的 before/after 传给 `ApproveDiff`，默认开启；危险命令附中文意图摘要 | 3 报告共识；审批是安全最后闸门，盲批=无审批 | M | 663c55e |
| P0-2 | **undo 过安全链**：TUI/CLI 的 undo 先 `chain.CheckRestore(paths)` 再执行 | 合规破口：「所有写操作过安全链」承诺的例外 | S-M | 663c55e |
| P0-3 | **关键事件不丢**：错误/重试/压缩/预算类事件走不可丢弃通道；压缩在 TUI 可见 | 压缩静默发生 → 用户以为丢上下文/卡死 | M | ea6b1ba |
| P0-4 | **首屏欢迎 + 上下文快捷键**：首帧注入欢迎卡（示例 prompt + `/help` + `Alt+U`）；对话框/审批态显示相关按键 | 破解「需要 help 的人不知道 /help」死锁 | S-M | ea6b1ba |
| P0-5 | **无 key fail-fast + doctor 凭据检查**：本地无凭据直接给三条具体路径，不发空 key 请求；doctor 加 credential 检查，401 视为「可达未认证」 | 首次失败的最常见原因，错误信息第一次就指对动作 | M | 2915666 |
| P0-6 | **接线已有友好建议**：`authExpiryAdvice`/`confirmAdvice`/`contextBudgetAdvice` 接到真实错误路径 | 已有函数+测试，零设计成本，立竿见影 | S | ea6b1ba |

### P1 — 接着做（日常顺手）

| # | 改动 | 成本 |
|---|---|---|
| P1-1 | 配置路径统一（消除「配了不生效」幽灵故障）；修正文档变量名 | 6b7c23f |
| P1-2 | `Ctrl+C`/`Esc` 语义统一（空闲二次确认退出；Esc 逐层关闭） | bdf9625 |
| P1-3 | 审批队列计数 + `a` 授权范围文案诚实化 + 防误触宽限期 | bdf9625 |
| P1-4 | 错误中文短句 + 可展开详情 + 关联动作 (`/retry`)；重试提示中文化 | bdf9625 |
| P1-5 | 工具阶段进度（长任务显示「在做什么 + 已用时 + 最近输出」） | `5ab7fa0` |
| P1-6 | 修 `/model` 错误文案（指向不存在的命令）；thinking 枚举与文档一致 | 6b7c23f |
| P1-7 | GUI 顶栏用真实 `config.info`；未连接引擎时显式横幅而非静默 demo | S-M | 顶栏真实信息已落地（`d73df72`）；demo 显式横幅待 native 代理 |
| P1-8 | GUI/会话空状态 + 会话行当前标记 + 重命名后刷新 | S | 空状态已落地（`d73df72`）；当前标记/重命名刷新待 native 代理 |

### P2 — 排期（大重构，需 RPC/状态机扩展）

| # | 改动 | 成本 |
|---|---|---|
| P2-1 | 会话 CRUD RPC（`session.new/rename/delete`）+ TUI 会话激活接缝 | L |
| P2-2 | undo/retry 后 transcript 同步重载（屏幕跟随磁盘状态） | L |
| P2-3 | 交互式 `argentgo init` 向导（可延后、分步选模型/登录） | L |
| P2-4 | checkpoint 式「Reset Chat / Reset Code」双选项 + 分支续跑 | L |
| P2-5 | GUI 11 个占位导航诚实化（置灰 + 即将推出角标） | S-M |

**P2 进度（2026-11-23 续）**
P2-5 完成：导航真实数据接入（用量/Git 分支/计划）已落地（`443df48`），174 native tests 全绿。
P2-1 完成：服务端 RPC（`138e642`）+ app sessionStore 接线（`58e59f8`），session.new/rename/delete 在生产路径可用。
P2-3 `init` 向导完成：`argentgo init` 四步交互（provider 探测/凭据/补写配置/doctor 自检），可延后、非 TTY 明确报错，1791 tests 全绿（`ddb6634`）。
**RPG 风格渲染改造**：用户提供 RPG Desktop 截图，要求 native 按其风格改造（三栏布局+紫色系+圆角卡片）。规范见 `GUI_STYLE_RPG.md`，实施中（native 代理）。
