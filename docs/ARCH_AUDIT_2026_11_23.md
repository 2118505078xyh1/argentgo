# 架构体检与修复报告（2026-11-23）

> 4 个只读代理并行审计当前架构（app 装配/生命周期、agent 循环复杂度、门控组合边界、Event 多协议契约），用真实源码证据核对用户提出的 5 个风险点。本文记录发现、判断与修复状态。

## 审计结论总览

用户提的 5 个风险里，**2 个被实测推翻、3 个是真实问题**：

| 风险假设 | 实测结论 |
|---|---|
| "lease/claims/reconciler 把 agent 循环写胖了" | ❌ 推翻——它们在 app/session 层经注入接口隔离，agent 循环无感知。循环是"职责多但有边界"，非上帝状态机 |
| "app 装配层有严重生命周期 bug" | ⚠️ 部分——defer LIFO 顺序正确、审计无丢日志、正常退出不丢会话尾；但发现一个**更严重的 P1**（见下） |
| "门控组合有顺序冲突" | ✅ 真实——4 个门控读点分裂（进程 env vs per-run env），多表面下开关失效 |
| "Event 多协议静默丢事件" | ✅ 真实——`goal_status`/`tool_arg_delta` 发了但 TUI/native 无 case |
| "并发 lease 与会话一致性" | ✅ 真实——本机 TTL 24h + PID 复用 → 崩溃后最长拒 24h；另有 claims 分类依赖稀疏视图 |

## 真实问题与修复状态

### P1-1 safetyOracle 进程级串味【已修 `60f133e`】
- **证据**：`bash/safety_oracle.go:23` 全局 atomic.Value；`app_execute.go:546` 每次 Execute 注入捕获本次 chain/ctx 的闭包，从不清除。代码明确预期并发 Execute（TUI+RPC），会 last-writer-wins 串味——运行 A 的 bash 用运行 B 的 chain/ctx 做安全判定。
- **修复**：改为 ctx 注入（`bash.WithSafetyOracle`），彻底删除全局 atomic.Value；200 goroutine 并发测试 + 两条不同 deny 规则并发 Execute 测试证明隔离。

### P1-2 门控读点分裂【已修】
- **证据**：`ARGENT_TURN_LEASE`(`session/turn_lease.go:42`)、`ARGENT_CONTEXT_CALIBRATION`(`app/context_calibration.go:18`)、`ARGENT_TOOL_PRUNE`、`ARGENT_FOVEATED_ARCHIVE` 读进程 env；`ARGENT_INSTRUCTION_RECONCILE`、`ARGENT_BACKGROUND_COMPACT` 读 per-run `stdio.Env`。
- **后果**：多表面（--serve/rpc/TUI）各带自己的 env 时，per-run 设 `ARGENT_TURN_LEASE=0` 关不掉——租约在 Execute 最开头获取，直接拒绝整轮。
- **修复**：给三个函数加 `env func(string) string` 变体（From），Execute 顶层用 `stdio.Env` 显式传入；原函数保留作进程 env fallback。
- **已知限制**：`Messages()` 内部的 `foveatedArchiveEnabled()` 仍读进程 env——per-run 关 foveated 拿完整视图的场景需走 `MessagesForCompaction()`，未改 `Messages()` 签名（避免大面积波及）。

### P1-3 Event 静默丢弃【已修 `42ced84`】
- **证据**：`goal_status`(`goal_loop_wire.go:130`)、`EventToolArgDelta`(`agent/events.go:38`) 发出后，TUI `AgentToTUIEvent` 与 native `applyEngineEvent`（只认 4 种）都无 case。--rpc 传输层全透传（`server/rpc.go:679`），不是协议层问题。
- **修复**：补 TUI/native case（`goal_status` 状态徽章、`tool_arg_delta` 工具卡片实时预览）；把两处硬编码清单改为 **AST 源码扫描穷举守卫**——新增 kind 未登记或声明 case 但源码无实际 case 都会让测试失败。实测注入探针常量验证守卫有效。

### P2-1 claims 分类依赖稀疏视图【已修】
- **证据**：`app_execute.go:335` 用 `sess.Messages()`（foveated 稀疏）分类恢复，中段证据丢失可能把 degraded 误判 clean → 关闭 claims → 崩溃恢复不去重非幂等工具。
- **修复**：一行改用 `sess.MessagesForCompaction()`，与压缩估算口径一致。

### P2-2 lease TTL + PID 复用【已修】
- **证据**：本机标记 TTL 24h，内容仅主机名无启动时间；Windows PID 复用 + `OpenProcess` 判活 → 崩溃后陈旧标记被误判活跃，拒绝新 turn 最长 24h。
- **修复**：本机 TTL 24h→60s（活跃租约每 20s 续期不受影响）；新增 `TestTurnLeaseLocalTTLBoundsStaleMarker` 锁定"PID 判活但超 TTL 必须回收"。

## 明确不做的"重构"（据实测避免为重构而重构）

- **拆 `runState` 82 字段**：多为 nil-safe 观测开关与并发锁，拆分破坏逐字节不变与顺序契约，风险高收益低。
- **拆 agent 主循环为多文件**：实测策略判定大多已下沉（`agentpolicy/`、`guard.go`、`verify_loop.go`、`DetectLoop` 等），唯一热点是 `loop.go:1150-1200` 回复续行状态机，可做小范围纯函数抽取（`DecideReplyContinuation`），非紧急。
- **改 defer LIFO 顺序**：审计确认 lease 最后释放是正确的（它保护会话 JSONL 的全部写入，含末尾图像卸载标记），不要"先释放 lease 再刷会话"。

## 验证与状态

- 全量基线：**10950 passed / 0 failed**；archguard 11、i18n 545、native 182 全绿。
- 5 个真实问题（P1×3 + P2×2）全部修复并验证；2 个风险假设（agent 循环变胖、app 生命周期严重 bug）经实测推翻。
- 所有修复遵循：核心 stdlib-only、无 cgo、分层不变（tools 不 import agent）、i18n 硬编码基线不增长。
