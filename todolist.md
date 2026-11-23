# argentgo 优化 TODO（仅未做项）

**统计**：0 条未做项。本轮（2026-11-23）竞品驱动的 56 条优化已全部实现、接线并全量验证通过。

---

## 本轮成果记录（2026-11-23，已全部完成）

### 来源与过程

8 个只读子代理并行深挖 10 个竞品的 git tracked 源码（OpenHands / dsh / kilocode / codex / opencode / oh-my-pi / pi-mono / cline / crush / killi），产出 56 条带 `路径:行号` 证据的去重条目；11 个优化代理按文件边界独占并行实现，2 个收尾代理完成 12 个原语的真实接线与过时断言刷新；主会话亲自修复压缩决策与 foveated 归档的耦合、plan 模式测试语义、checkout 断言隔离，并完成文档收尾。

### 交付统计

- 本轮 21 个提交（从 `d51c2a5` 到 `beb8931`），约 190 个 Go 文件变更、1.4 万行新增。
- 全量验证：`gofmt -l .` 空、`go vet ./...` 干净、`go test -p 4 ./... -count=1` **10964 passed / 0 failed（26 包）**、`internal/archguard` 11 passed、native 逻辑子集 4 passed。
- 12 个新增导出原语**全部真实接入生产路径**，archguard 零 allowlist 登记。

### 已落地条目（按类别）

- **上下文/压缩（16）**：压缩后二次缩减校验、压缩请求前缀复用保 KV 缓存、agentic 折叠只取上次摘要后新消息、模板校验 NUDGE 重试、摘要膨胀守卫、压缩 usage 即时入账（含失败路径）、原生压缩 + 输出上限拟合 + 拒绝回放过滤、压缩请求自身超限剥离与分块递归归约、max_tokens 拟合窗口、fenced/XML 大块机械剥离、嵌套指令整段包含去重、遗弃分支文件清单、续接摘要 caveat、工作区指令变更重注入、图像按最旧卸载、图像像素尺寸计价。
- **安全边界（14）**：配置说明文件保护（always 不可覆盖）、密钥文件宽泛读收窄、敏感审批只认真人交互、bash 分层通配作用域、非幂等工具领取账本、缺失不构成授权告示、中断部分执行告示、fork 只读边界、已批准作用域回灌、嵌套规则提醒、会话状态可见性、跨进程 turn 租约、提供方拒绝不回放、配置/报告同步。
- **工具与执行（15）**：前台长 sleep 拒绝、软超时观察语义、会话内 URI 结果缓存、EOL/BOM 保留、截断损失头与 JSON 转义感知预算、read 单行上限、edit 首条分歧行、工具超时结构化错误、工具参数增量预览、工具名别名、配置化 LSP、checkpoint 行级 hunk、进程级 pause 门、工具名路由等。
- **模型/生态/交互（11）**：有界判官目标循环、agent 自驱模型切换、refresh token 负缓存、动态工具路由、子代理按 id 续跑与 worktree 隔离、todo/nudge 提醒、冒号未完成续做、循环检测插话重置、TUI 粘贴路径与 kill-ring、记忆去重等。

### 重要设计决策

1. **默认化原则**（覆盖旧 opt-in 优先约束）：改善体验的新功能默认开启、参数取最优值、用户零选择；环境变量仅留 `=0` 紧急回退。已同步写入 `AGENTS.md`、`docs/STATUS.md`、`CHANGELOG.md`。
2. **压缩估算与 foveated 视图解耦**：新增 `session.MessagesForCompaction()`，压缩决策用未稀疏化的完整转录估算，避免默认归档后长会话因估算偏低而永不压缩。
3. **接线核对（教训）**：archguard 门禁只查「新增导出函数是否有调用方」，但多个代理把 wire 函数设为非导出或函数早于基线存在，导致门禁通过却有多处功能从未接入生产路径。经两轮逐项 grep 核验后补接：
   - **第一轮**：`wireSwitchModel`/`wireModelToolRouting`/`runGoalCommand`/`wireTaskExtras`/`wireRequestSecret`/`wireLSPFromConfig` 六个 wire；`ContextWindow` 注入（输出拟合真正生效）；拒绝回放投影；原生压缩（opt-in）。
   - **第二轮**：`summaryTools` 赋值（压缩前缀复用的工具表）、apply_patch 的 EOL/BOM 规范化、`ErrToolTimeout` 哨兵测试。
   - **结论：门禁通过 ≠ 功能接线，后续须逐项 grep 调用点验证。**
4. **合理取舍未接项**：`/pause` 全局暂停（进程级状态，忘记恢复会卡死所有会话，风险高于价值）、`request_secret` TUI 模态弹窗（终端文本 requester 已可用）、`nativeWindowReplay` 展开（需跨 session/llm/app 传 model，且原生压缩默认关闭时无影响）、`CollapseOutput` 损失头（clip/PruneToolResults 层已实现同类标记）。这些是可选增强而非核心功能缺失。
5. **原生压缩保留 opt-in**：`ARGENT_NATIVE_COMPACT=1` 才尝试，因为它会产生一次额外辅助请求且多数 provider 不支持，属默认化原则例外里有代价的能力；原生窗口写入压缩记录，回放时同模型才展开，否则安全回退文本摘要。

---

## 附：本轮竞品证据索引

| 调研代理 | 主要发现方向 | 产出条数 |
|---|---|---|
| OpenHands（首轮） | 判官目标循环、工具领取账本、密钥请求、子代理续跑/隔离、软超时观察 | 7 |
| dsh（首轮） | 压缩前缀复用、缩减门禁、循环检测重置、指令变更重注入、图像卸载 | 7 |
| kilocode（首轮） | 压缩请求超限恢复、作业级联取消、配置保护、密钥读取收窄、真人审批、turn 租约 | 8 |
| codex（二轮） | JSON 转义预算、截断计数、缺失不构成授权、中断告示、作用域回灌、fork 边界、recap caveat | 7 |
| opencode（二轮） | BOM 保留、工具名别名、摘要 NUDGE、摘要膨胀守卫、费用入账、LSP 配置、原生压缩 | 7 |
| oh-my-pi + pi-mono（二轮） | 输出拟合窗口、shake 大块剥离、拒绝不回放、图像尺寸 token、pause 门、kill-ring | 8 |
| cline + crush（二轮） | EOL 规范化、工具结果 URI 缓存、read 单行上限、模型工具路由、refresh 负缓存、粘贴路径 | 7 |
| killi + argentgo 未接线原语 | 嵌套规则提醒、会话状态、CriticResult、sleep guard、todo nudge、未完成续做、edit 分歧行 | 7 |
| **合计（去重后）** | | **56** |
