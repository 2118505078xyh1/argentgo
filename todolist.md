# argentgo 优化 TODO（100 条）

> **生成方式**：2026-11-22，10 个只读子代理并行深挖 git tracked 源码（HEAD `8881ed9`，round 48），按 9+ 维度取证，交叉去重后按用户优先级重排。子代理全程只读，未改动任何文件。
>
> **证据口径**：每条都附 `路径:行号 / 函数名`，区分「已实现但未接线 / 仅桩 / 边界 bug / 缺测试 / 缺验证」。路径相对项目根 `D:\code\mpi\argentgo\`。
>
> **硬约束**（所有建议必须满足）：核心 stdlib-only、零新增 Go 模块、无 cgo、单二进制、自研 shell 不外包；`native/` 是唯一 MyGo 例外；opt-in 项默认关闭且默认路径逐字节不变是项目承诺。
>
> **优先级**：P0 = 实锤 bug / 安全绕过 / 数据丢失 / 发布阻塞；P1 = 已有原语未接线 / 明显体验缺口；P2 = 改进与防御纵深。工作量 S/M/L。

**统计**：P0 ×11，P1 ×60，P2 ×29（共 100 条）。

---

## 一、上下文管理与可恢复性（1–14）

### 1. `checkpoint_diff` 空串/`latest` 必然报错 `[P0/S]`
- **现状**：`internal/app/checkpoint_source.go:20-31` 注释声明「空 id 表示 latest」，但 `steps` 默认 0，随后 `c.sess.CheckpointBack(steps)`（:31）进入 `internal/session/checkpoint.go:236-238`，因 `steps<1` 直接返回 `checkpoint steps must be >= 1`。而 `internal/tools/tools_checkpoint_diff.go:69` schema 明确允许 `checkpoint_id:""`，集成测试也用空串调用（但注入 fake source，真实适配器无人测）。
- **问题**：模型获取「最近检查点差异」这一最常见用法恒定失败，而 `LatestCheckpoint()`（checkpoint.go:229）明明可用。已有原语 + 边界 bug + 缺测试三合一。
- **建议**：`Diff` 中把 `id=="" || id=="latest"` 显式映射到 `LatestCheckpoint()` 分支，仅显式数字串走 `CheckpointBack(steps)`；`steps==0` 给稳定错误文案。
- **验收**：新增 `internal/app/checkpoint_source_test.go` 的 `TestCheckpointSourceDiffLatestAndEmpty`，断言 `Diff("")==Diff("latest")==Diff("1")` 且 `len(changes)>0`；`go test ./internal/app -run TestCheckpointSourceDiff -count=1`。

### 2. 轮次内 Progress 检查点「写了不读」`[P0/M]`
- **现状**：`internal/session/attempt.go:117,157` 的 `Progress`/`AppendProgress`，生产侧唯一写入口 `sync.go:49-55`；`repair.go:254-291` 的 `RecoverProgress`（含两道过期检查）**只在测试中出现**，`internal/agent`、`internal/app` 零引用。
- **问题**：「中途崩溃从最后进度点恢复」的设计完全没兑现；每轮请求前的持久屏障都在写进度记录，重启后却被当普通记录忽略，纯写放大；长文本轮次崩溃丢失全部已流式输出。
- **建议**：在 `internal/app/app_execute.go` 恢复路径（已有 `recoveryMessagesForClassification`:676）调用 `sess.RecoverProgress()`，经新 opt-in `ARGENT_PROGRESS_RECOVERY` 把 `Progress.AssistantText/ToolCalls/Turn` 注入 `SeedContext` 或 `EventNotice`；注册进 `internal/cli/optin_metadata.go:42`。
- **验收**：新增 `internal/app/progress_recovery_wire_test.go`：写 `Progress{Turn:2,AssistantText:"部分输出"}` 未提交即崩溃，重开断言公告含该文本；已提交 assistant 后断言返回 false；`go test ./internal/app -run TestProgressRecovery -count=1`。

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

### 11. 非破坏性 `context_edit` 记录节点 `[P0/M]`（学自 pi-mono）
- **竞品证据**：`pi-mono/packages/coding-agent/src/core/session-manager.ts` 的 `ContextEditEntry`（`targetId`+`replacement`，:175）、`appendContextEdit`（:1378）、`projectContextEntry`（:520）。
- **现状/缺口**：`internal/session` 的只追加 JSONL、`Checkout`、`Compact` 只支持整段丢弃/替换；发现某轮注入错误上下文时只能 fork 重来，没有「从模型上下文隐藏/替换单条内容而不改磁盘历史」的能力。
- **建议**：新增记录 `kind:"context_edit"`（`targetId`/`replacement`，沿用 id/parent 与脱敏边界）；在 replay 投影入口按 targetId 应用「最后一条生效」覆盖；新增 CLI `sessions edit-context <id> <entry> [--text|--hide]` 与 RPC `session.editContext`；不触碰既有记录字节。
- **验收**：插入后 replay 中目标被隐藏/替换而 JSONL 原文不变；多次 edit 同一 target 最后一条生效；fork/checkout 后覆盖归属正确；未使用时 golden 逐字节不变。

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

### 14. TTSR 流中条件规则注入 `[P1/L]`（学自 oh-my-pi）
- **竞品证据**：`oh-my-pi/packages/coding-agent/src/export/ttsr.ts` 的 `TtsrManager.checkDelta`:858/`checkSnapshot`:880、`JUDGED_RULE_THRESHOLD=0.7`，命中即中止流→注入 system reminder→重试。
- **现状/缺口**：`internal/hooks` 的 11 事件 + exit-2 阻断只在工具边界；`steer.go` 在轮次边界；`prompt_injection_scan.go` 只警示。没有「输出文本命中条件→立即中止流→注入提醒→重试」闭环。
- **建议**：`internal/agent` 加 `RuleWatcher`（opt-in `ARGENT_TTSR=1`），规则来自 JSON（scope=text/thinking/tool、condition=正则、action=warn/inject/retry）；复用 `compact_retry.go` 的中止+重放骨架与 `tool_result_wrapping.go` 的注入格式；非法正则跳过并告警。
- **验收**：命中后流被中止且每轮最多重试一次；注入文本进入下一轮 system 但不覆盖 files/failures 权威字段；默认关闭逐字节不变。

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

### 25. Guardian 从 fail-closed 占位接到真实次要模型 `[P1/M]`（学自 codex）
- **竞品证据**：`codex/codex-rs/core/src/guardian/review.rs` 的 `select_synchronous_reviewer`/`run_before_review_deadline`/`guardian_output_schema`。
- **现状/缺口**：`internal/app/guardian_wire.go` 的 `defaultGuardianReview` 永远返回 `RequireApproval`，注释明写「真正的廉价模型审阅尚未接入」；`guardianInspectorFor` 已留注入缝，`internal/security` 已有 verdict 类型但缺真实实现。
- **建议**：实现 `guardianReviewFunc`，用 `secondary-model` + 现有 `Chatter.ChatOnce` 非流式短答（2–3s、小 maxTokens）对「工具名+规范化参数+工作区上下文」做 allow/require/escalate 判定；输出限定 JSON 并严格校验，schema 失败/超时/空模型一律 RequireApproval（fail-closed）；沿用 `ARGENT_GUARDIAN=1` 双开关。
- **验收**：三种 verdict 各有表驱动测试；超时/畸形 JSON/无次要模型均 RequireApproval；关闭时请求字节 golden 一致。

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

### 30. 持久化 PTY shell 会话：`cd`/`export` 跨调用 `[P1/M/L]`（学自 opencode）
- **竞品证据**：`opencode/packages/core/src/persistent-pty/index.ts` 的 `PersistentPty`、`daemon.ts` 的常驻传输（`ProtocolVersion=9`）。
- **现状/缺口**：`internal/pty` 有原语但 `internal/shell/tools_builtin_bash.go:131` 的 `bashTool.Run` 每次 `exec.Command` 新进程，`cd`/`export`/venv activate 不跨调用保留，模型每轮重复环境前置。#29 解决接线，本条解决会话持久化这一增值能力。
- **建议**：复用 `internal/pty`+`internal/jobs`，在 `internal/shell` 增加按 workspace+shell kind 键控的 `ShellSessionRegistry`（长驻进程+空闲超时+输出环形缓冲，复用 output_spill）；bash 工具加 `session`/`persist` 参数；通过注入 `pwd`/env 探针回读校验状态；opt-in `ARGENT_PERSISTENT_SHELL=1`。
- **验收**：连续两次调用第二次继承 cwd/env；空闲超时回收无残留进程；取消不泄漏；关闭时输出逐字节不变。

### 31. proc idle watchdog 孤立 + SubstBudget 未接线 `[P1/M]`
- **现状**：`internal/proc/idle_watchdog.go:22` 的 `idleWatchdogExpired`（含单调时钟语义）全仓唯一调用方是测试；`proc.go:225-271` 的 select 只有 done/总超时/ctx，无 lastActivity 更新。展开侧 `internal/llm/subst_budget.go:5-8` 自述「没有内置调用方」，`Enter`:51/`Exit`:68 仅 guard 测试；对照 `llm/sse_idle.go:17` 已正确接线。
- **问题**：先输出几行后彻底卡死（等网络/死锁）的命令，只要总超时长（上限 3600s）就一直挂到总超时或人工取消；深层命令替换 `$()` 无深度上限，防御-in-depth 失效。
- **建议**：① `Command` 增 `IdleTimeout`，输出 writer 包 `atomic.Int64 lastActivity`，select 加 `time.Ticker` 周期调 `idleWatchdogExpired`，命中走 killTree 路径并返回新哨兵 `ErrIdleTimeout`；`shell.Interp.Timeout` 旁加 `IdleTimeout` 透传。② shell 展开路径构造 `SubstBudget`，递归前 `Enter()`、失败返回受限错误、`defer Exit()`。
- **验收**：`internal/proc/idle_watchdog_run_test.go`：假子进程先写一行后永不退出，`IdleTimeout=50ms`/`Timeout=10s`，断言 200ms 内返回 `ErrIdleTimeout`；持续输出的进程 200ms 后仍存活。`go test -race ./internal/proc -run Idle -count=1`。

### 32. `tool_argument_coercion` 死代码 + 审批参数错位 `[P1/M]`
- **现状**：`internal/agent/tool_argument_coercion.go:23` 的 `SetToolArgumentCoercion` 全仓零调用，`dispatch.go:491` 门控恒 false；文件注释称「由 app 层启动时设置」但 `app_execute.go:499-542` 无该调用。另一方面 `dispatch.go:417` 审批用原始 `call.Function.Arguments`，:489-493 的 `ExtractIntent`/`coerceToolArgumentsJSON` 在审批后的 `runTool` 才改写实际参数。
- **问题**：完整实现并测试的 250 行强转能力是死代码；更关键的是顺序错误——若按注释接线，用户批准 A 参数（`{"timeout_seconds":"5"}`）落到工具的却是 B（`{"timeout_seconds":5}`），`approvalDigest` 与执行不可对应，审计与可追责性被破坏。
- **建议**：① `app_execute.go` 按 env `ARGENT_TOOL_ARG_COERCION=1`（注册进 optin_metadata）调用 `SetToolArgumentCoercion`。② 把强转上移到 `executeCall` 审批之前，审批与 `approvalDigest` 都用同一份规范化 `toolArgs`，`runTool` 只消费不二次改写。③ 若不启用则删除死代码或明确登记「保留未启用」。
- **验收**：`internal/agent/approval_args_parity_test.go`：开启 coercion，断言 `Approver.Ask` 收到的 args 与 `ToolRunner.Run` 收到的逐字节相等。

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

### 54. 渲染缓存原语 `frameCacheKey` 已写好但 run 循环未消费 `[P2/M]`
- **现状**：`internal/tui/frame_cache.go:14,57,121,129` 的 `frameCacheKey`/`newFrameCacheKey`/`frameCacheHit`/`isPureScrollChange` 已实现且有表驱动测试，但注释 :9-13 明确「run 循环中尚无消费者」；`run.go:114-131 frameWriter.draw` 只依 `m.Dirty()` 决定 Paint，每 50ms tick 仍无条件 Flush。
- **问题**：时间敏感状态（spinner/通知/实时推理行）持续标脏触发完整 Paint，`bodyLinesLocked`/`headerLocked` 计算未省；已有安全缓存键零收益。
- **建议**：`run.go:106 frameWriter` 增 `lastKey frameCacheKey`/`haveKey bool`；draw 中先算 key，命中则跳过 `m.Paint`（保留 Flush 与光标差异）；force 时强制重绘；注意 `newFrameCacheKey` 自带锁须在 Paint 锁外调用。
- **验收**：`run_test.go` 注入 spy writer，连续两次相同状态 + tick，断言第二次 Paint 调用为 0、写字节为 0；`go test ./internal/tui -run 'TestFrameCache|TestDraw' -count=1`。

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

### 58. Native 审批卡无 diff，文件编辑审批近乎「盲批」`[P2/M]`
- **现状**：`native/main.go:932-954 approvalView` 仅显示 `ap.name`/`ap.args` 与「Y 允许·N 拒绝」，无变更文件/增删行/前后对照；TUI 侧 `internal/tui/approval_diff.go:41,53,166` 的 `ApprovalDiff`/`BuildApprovalDiff`/`RenderApprovalDiff` 已实现并由 `app/tui.go:57 ConfigureApprovalDiff`+env `ARGENT_APPROVAL_DIFF=1` 门控；native 无对应 env 读取。
- **问题**：桌面端是主要 GUI，用户审批「改文件」时看不到任何 diff，只能凭工具名和参数放行，与 TUI 体验严重不一致，属高风险可用性缺口。
- **建议**：在 `native/` 新建 `approval.go` 实现与 `BuildApprovalDiff` 等价的纯函数（native 禁 import internal，按 `bridge.go:13-16` 约束复制最小逻辑）；若 agent 事件不带 diff 则在 `main.go:1156 engineApproval` 加可选字段并确认 server payload；`approvalView` 在 args 下方加一行摘要 + 有界预览，加 `ARGENT_NATIVE_APPROVAL_DIFF=1` 门控保持默认帧不变。
- **验收**：`native/approval_diff_test.go`：一改一增快照断言摘要含文件数与 +/− 计数、预览行数有上界、CJK 路径不切断、门控关闭时渲染元素数不变。

### 59. Native bridge 断连无用户可见提示 `[P1/M]`
- **现状**：`native/bridge.go:157-166 readLoop` 在 EOF/读错误时置 `b.closed=true` 并给 pending 回 "engine connection closed"，但 bridge 结构体（:17-32）无 onClose/状态回调；`main.go:1485-1490` 仅启动时设一次 `app.status="已连接引擎"`；下一次提问经 `sendReal`→`call` 直接返回 "engine bridge closed"，才由 `completeFromPrompt` 显示为一条错误气泡。
- **问题**：engine 崩溃/被杀后桌面窗口仍显示绿色「已连接引擎」，用户继续输入才收到失败提示，且无一键重连。
- **建议**：bridge 加 `onClose func(error)` 与 `setCloseHandler`（复用 setDeltaHandler 的 mu+generation 模式），readLoop 解锁后调用；main 注册回调通过 `app.win.Update` 把 status 改为「引擎连接已断开」、状态点变 danger，并在 composer 旁渲染「重新连接」按钮（重走启动逻辑）。
- **验收**：`native/bridge_close_test.go` 用 pipe 模拟 EOF，断言 onClose 恰好一次且 status 含「断开」；`go test -race ./native -run TestBridge -count=1`。

### 60. `argentgo gui --locale` 被解析却未传给原生窗口，开关静默失效 `[P2/S]`
- **现状**：`cmd/argent/gui.go:37,44` 声明并注册 --locale，`:58-61` 塞进 `cli.Options.Locale` 交给 app（影响 engine），但 `:84-98 launchNativeFrontend` 的 `cmd.Env`（:97）只设 ARGENT_EXE/ARGENT_CWD；`native/main.go:1480-1485` 只读取这两个变量，chat 无 locale 字段。
- **问题**：用户传 --locale 期待切换桌面语言，实际只影响后端，无任何警告；TUI 走 opts.Locale 而 native 走 env，两条路径无统一契约。
- **建议**：`gui.go:97` env 追加 `"ARGENT_LOCALE="+resolved.Locale`；native 初始化 `chat{locale: os.Getenv("ARGENT_LOCALE")}` 并经 #52 的适配层解析；若明确不支持英文至少打印警告避免静默。
- **验收**：`cmd/argent/gui_test.go` 断言 env 构造含 ARGENT_LOCALE；`native/main_test.go` 设 en-US 后 app.locale 解析为 en；`go test ./cmd/argent ./native -run 'TestGUI|TestLocale' -count=1`。

### 61. TUI 自建 `locale.go` 双表与 `internal/i18n` 并行，两套目录未统一 `[P2/M]`
- **现状**：`internal/tui/locale.go:10` 的 `localeStrings`（en/zh 双表 ~58 键）与 `:139 text` 是 TUI 唯一文案来源；`internal/i18n/i18n.go:33 catalog` 是另一套 zh-CN/en-US 表；TUI 仅在 `activity_summary_test.go` import 过 i18n；`i18n.go:1-5` 自称覆盖 TUI 但与事实不符。
- **问题**：新增用户可见字符串要在两处各写一遍，键名/译文易漂移；i18n 的 `hardcoded_scan_test.go` 守卫覆盖不到 TUI 文案，形成治理盲区。
- **建议**：统一到 `internal/i18n` 单一 catalog：把 TUI 的 58 键迁入并以 `i18n.Tr(locale,key)` 替换 `text()`；保留 `tui/text` 作为薄转发以减小 diff；扩展 `hardcoded_scan_test.go` 扫描 TUI 新增硬编码（与 #52 的 baseline 方案合并）。
- **验收**：`grep -rn 'localeStrings' internal/tui` 仅剩薄转发定义；两语言 Frame 输出与迁移前逐字节一致；i18n 守卫覆盖 TUI 目录。

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

### 81. 「行数阈值才发版」没有机器校验，HEAD/行数文档已过期 `[P1/M]`
- **现状**：`AGENTS.md:121-141` 定义锚点表与「达到阈值且全绿才发版」，:123 写死 `HEAD a30b0e4=355,578 行`；`CHANGELOG.md:3` 写 `HEAD 7dee8a1`；`check-release-consistency.ps1:13-33` 只正则比对 Version/tag/CHANGELOG 段落不统计行数。实测 HEAD 8881ed9a、tracked Go 约 409k 行（两处文档 HEAD 与行数均过期）。
- **问题**：发布规则第 1 条完全没有强制者——50w 未达到时打 v0.8.2 tag，三脚本全绿 Release 正常产出；审计者按文档复盘得到错误基线。
- **建议**：check-release-consistency.ps1 增加 `git ls-files -z '*.go'`+逐行累加的 PowerShell 统计，把 AGENTS.md 锚点表抽成单一真源文件（如 `docs/release-anchors.json`），按 $version 查阈值，小于则 throw；CHANGELOG.md:3 与 AGENTS.md:123 改为不写死哈希/行数。
- **验收**：当前树 `-Tag v0.8.1` 退出 0；`-Tag v0.8.2` 在 409k 行下必须非零退出并打印行数不足；`grep -n 'a30b0e4\|7dee8a1' AGENTS.md CHANGELOG.md` 为空。

### 82. Fuzz 只跑种子且只覆盖 security；会话恢复属性与真正 fuzz 引擎从未接线 `[P1/M]`
- **现状**：`scripts/security-audit.ps1:76-79` 仅 `go test ./internal/security -run Fuzz -count=1 (seeds only)`；CONTRIBUTING.md:175-176 只是手工文档；全仓 `grep -fuzz` 仅命中文档与 fuzz_test.go:12,315 注释；9 个 Fuzz 目标中 `internal/session/recovery_fuzz_test.go:46 FuzzSessionRecordRoundtrip`（属性见 :13-18）从未被 CI/脚本执行；`git ls-files` 无 `testdata/fuzz/` 落盘语料。
- **问题**：最脆弱的 JSONL 容错修复路径与脱敏/通配符只被固定种子覆盖，变异输入触发的 panic、错误恢复语义无门禁；崩坏语料无法积累。
- **建议**：新增 `.github/workflows/fuzz.yml`（schedule 每周 + workflow_dispatch），对 9 个目标逐一 `go test -run=^$ -fuzz=^FuzzXxx$ -fuzztime=60s`，fail-fast:false，失败时把 `testdata/fuzz/<Target>/` 上传 artifact；security-audit.ps1 的 fuzz 步骤扩为 security+session 两包。
- **验收**：`go test ./internal/session -run=^$ -fuzz=^FuzzSessionRecordRoundtrip$ -fuzztime=30s` 退出 0；`git ls-files | grep 'testdata/fuzz/'` 至少一条。

### 83. 三处 race 门禁中两处无 `-timeout`，挂起会耗尽 6 小时 job 上限 `[P1/S]`
- **现状**：`ci.yml:81`、`release.yml:100` 的 `go test -race ...` 无 timeout，只有 matrix.yml:97 带 `-timeout 20m`；ci.yml 的 race job 在 ubuntu-latest 上还包含 native 包（`go list ./...` 未过滤），与 AGENTS.md「native 偶发 GPU 挂起」风险叠加。
- **问题**：一旦死锁/长时间阻塞，release tag 流水线的 race 门禁最长挂到 GitHub 默认 6 小时才失败，阻塞发布且浪费 runner；三处命令形式漂移增加维护成本。
- **建议**：给两处补 `-timeout 20m`，把三处命令抽成 `.github/workflows/_race.sh` 或 reusable workflow 杜绝漂移；复核是否需要再排除 ./native（若在 Linux 确需排除，三处同步并注明 Windows 的 ci.yml go job 已覆盖 native 测试）。
- **验收**：`grep -n -- 'go test -race' .github/workflows/*.yml` 三处均含 -timeout；test-release-workflow.ps1 新增静态断言 `race job pins -timeout`；用临时 `select{}` + 短超时验证门禁会失败。

### 84. `verify-release.ps1` 不校验产物二进制自报版本，native 版本无断言 `[P1/M]`
- **现状**：`scripts/verify-release.ps1:31-93 Assert-ReleaseArtifacts` 只验 zip 存在、含两 exe、checksums.txt 单条且 SHA-256 相等（:42/:59-64/:75-92）；native 源码 grep Version 命中 0（不自报版本）；check-release-consistency.ps1 只校验源码常量。
- **问题**：源码版本一致 ≠ 产物版本一致；若构建用陈旧缓存/错误 GOFLAGS，zip 与 checksum 全绿，`gh release create` 照常发布一个自报旧版本的 exe——反向风险（发错版本）没有门禁。
- **建议**：verify-release 在 Expand-Archive 后（:57 同 try 块）执行解包出的 `argentgo.exe -V` 并断言输出等于 `argentgo $Ver`，不匹配 throw；native 要么通过 `--version`/RPC config.info 暴露同一 cli.Version（ldflags -X 注入），要么断言 PE 文件版本资源与 $Ver 一致；release.yml:143-150 后加 "Smoke test binaries" 步骤。
- **验收**：用 test-release-workflow.ps1:209-217 伪造含错误版本输出的 zip，verify-release 必须非零退出；正常 FullBuild 下 `argentgo.exe -V` 输出 `argentgo 0.8.1`。

### 85. `flake-guard` 默认包列表遗漏近期并发热区且脚本未接线 `[P2/S]`
- **现状**：`scripts/flake-guard.ps1:19-24` 默认 `$Packages = @('internal/tui','internal/hooks','internal/jobs','internal/agent')`，:61-85 实现健康（-count=N，Race 默认关）；`.github/workflows` 无调用；CHANGELOG.md:18 记录 R43 在 internal/server 补「四处 fire-and-forget goroutine panic 恢复」、session JSONL Close fsync，这两包都不在默认列表。
- **问题**：最容易出 timing flake 的新增并发点（server goroutine、session fsync）恰恰没有重复执行门禁；脚本再好也因未接线零收益。
- **建议**：默认列表扩为 tui/hooks/jobs/agent/server/session/mcp/native；matrix.yml 增 flakeguard job（workflow_dispatch + schedule 每周，避免每个 PR 都慢）运行 `-Count 5`；如需 Race 单列一个 job 并保留「慢门禁只在发版/定时跑」约定。
- **验收**：`pwsh -File scripts/flake-guard.ps1 -Count 3` 退出 0 且打印 PASS internal/server / PASS internal/session；`grep -n flake-guard .github/workflows/*.yml` 非空。

### 86. 依赖锁定无一致性校验：白名单只查名字不查版本与哈希 `[P2/S]`
- **现状**：`go.mod:5-11` 直接依赖 mygo v0.10.0 + 5 indirect，go.sum 仅 14 行；`security-audit.ps1:84-135` allowlist 只对 `go list -m all` 的模块名字白名单，不核对版本；`:141-154` 只查 import 路径；全仓 `grep 'go mod tidy\|go mod verify'` 命中 0，三个 workflow 也无相关步骤。
- **问题**：把 mygo 改成恶意 v0.10.1 并更新 go.sum，白名单照样绿灯；go.sum 缺项或残留也不会发现。供应链审计只覆盖「有没有新模块」不覆盖「锁有没有被篡改」。
- **建议**：security-audit.ps1:136 后插两个硬失败检查 `go mod verify` 与 `go mod tidy -diff`（Go 1.27 支持 -diff 不改写文件）；allowlist 从名字数组升级为 `"module@version"` 映射，mygo 直接依赖固定 v0.10.0 精确校验；matrix.yml unit job 加同一步。
- **验收**：当前树 `go mod verify` 输出 all modules verified、`go mod tidy -diff` 输出为空；改 go.sum 一位哈希后脚本必须失败；改 mygo 为 v0.10.1 后脚本因版本不符失败。

### 87. 本地一键发布的门禁弱于 AGENTS.md 规范 `[P1/M]`
- **现状**：`scripts/build-release.ps1:69-105` 的 quality gates 只有 `go vet ./...`、`go test ./...`（无 -p 4、无 -count=1）、LF 归一化 gofmt；无 `go test -race ./...`、无 `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./...`；AGENTS.md:113-121 要求「全量验证全绿」明确含 -race 与 GOOS=linux；`publish-win.ps1:96-105` 直接调 build-release 后 push tag（:113-119）。
- **问题**：本地一键发布可在未跑 race 与交叉编译情况下 push tag 触发 Release；-count=1 缺失还允许 Go 测试缓存掩盖回归。
- **建议**：build-release.ps1 把 `go test ./...` 改 `go test -p 4 ./... -count=1`，新增 `-race` 与 `GOOS=linux ... go build` 两步；为免日常 -DryRun 过慢加 `-FullGate` 开关并让 publish-win.ps1 默认开启（`-SkipChecks` 才绕过）；test-release-workflow.ps1 加对应断言。
- **验收**：`powershell -File scripts/build-release.ps1 -FullGate` 日志依次出现 -count=1/-race/GOOS=linux 三步且退出 0；publish-win.ps1 -DryRun 日志显示 FullGate 已启用。

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

### 90. `model-quality.json` 缺失，长任务/测量质量原语未接线 `[P1/M]`
- **现状**：`internal/eval/long_task.go:9` 自述「真正的脚本化 provider 场景数据由后续填充」，`EvaluateTask`:74/`TaskCompletionRate`:132 只有纯函数测试；`quality_report.go:7` 明写「不接线到 CLI」，`AssessMeasurementQuality`:32 同样孤立；evals/ 目录 7 个 JSON 中无 model-quality.json，全仓 grep `model-quality|model_quality` 零命中；STATUS.md:128 承认「真实模型通过率无法从源码判定」。
- **问题**：真实模型评测目前只覆盖 completion 完成率（file_equals + go test 退出码），长任务 transcript 顺序核对与「样本量/显著性/监控盲区」汇总对用户不可见；产品名 model-quality 在文档中承诺却无实体。
- **建议**：新增 `evals/model-quality.json`（长任务 steps + 场景元数据 + 期望完成率），eval 加 `LoadModelQuality`/`RunModelQuality` 串起三个原语；经 CLI 新前缀 quality（或并入 --live）输出含 SampleCount/IncompleteReason/Warnings；配 fixture 锁数据判据一致。若暂不做则在 CAPABILITY_MATRIX §4 ⚠️-3 登记「long_task/quality_report 为未接线原语」并加文档守卫。
- **验收**：`go test ./internal/eval -run 'TestModelQualityData|TestAssessMeasurementQuality'` 绿；`argentgo eval --live evals/model-quality.json`（配 key）输出 completion_rate 与 Warnings。

### 91. `core.json` 场景数文档写 10、代码断言 11 `[P1/S]`
- **现状**：`internal/eval/eval_test.go:233-249` 断言 want 11、report.Passed!=11、输出含 `"1..11"`；`evals/core.json` 实际 11 场景（第 11 个是 286-316 行 approval denial）；但 `docs/CAPABILITY_MATRIX.md:209`、`:274`、`:287` 三处均写「10 个脚本场景」/「core.json（10 场景）」/「core.json 10 场景」。
- **问题**：用户按文档找「第 10 个场景」会错位；发版门禁实际跑 11 个，对外评测规模口径失真。
- **建议**：三处 10 改 11；参照 OPT_INS.md:5 的双向守卫范式新增 `internal/eval/core_docs_guard_test.go`——从 core.json 读场景数，正则核对 CAPABILITY_MATRIX.md 中的数字，防再次漂移。
- **验收**：`grep -n '10 场景\|10 个脚本场景\|core.json 10' docs` 无命中；`go test ./internal/eval -run TestCoreDocsGuard` 绿，临时加一场景时守卫失败。

### 92. go install/Homebrew 二进制名不一致，占位符版本格式自相矛盾 `[P1/M]`
- **现状**：go.mod 模块末段 argentgo 但导入路径末段 `argent`（`cmd/argent/main.go:2`），`go install .../cmd/argent@latest` 产出 `argent`（Homebrew 配方 :22-23 断言 `bin/"argent"` 为证）；但 `USER_GUIDE.md:84` 称「生成的 argentgo.exe 放 $GOBIN」并用 `argentgo.exe --version` 验证，README.md:33-37 同；`USER_GUIDE.md:362` 定义 `{{VERSION}}` 为纯数字，`homebrew/argentgo.rb:19` 写 `@{{VERSION}}` 而 :379 又自承 tag 带 v 前缀要填 v0.1.1，同文件两处冲突。
- **问题**：用户按 USER_GUIDE 装完运行 `argentgo --version` 会「命令未找到」，首次体验卡在验证步；Homebrew 装出的命令是全项目品牌不一致的 `argent`。
- **建议**：保留 cmd/argent 路径，① USER_GUIDE:76-86 明确「go install 产出名由导入路径末段决定」并给品牌命令 `go build -o argentgo.exe ./cmd/argent`；② Homebrew 用 `bin.install "argent" => "argentgo"` 并把 `@{{VERSION}}` 改 `@v{{VERSION}}` 同步 :379；③ verify-release.ps1 或新增 check-packaging.ps1 加 `grep -R '{{VERSION}}\|{{SHA256}}' packaging/` 应为空的残留检查。
- **验收**：临时 GOBIN 下 go install 后名字与文档一致；`ruby -c packaging/homebrew/argentgo.rb` 通过；干净 PATH 下执行 USER_GUIDE 验证命令成功。

### 93. 首次启动引导缺失，`cli_hints` 原语空挂 `[P2/M]`
- **现状**：`USER_GUIDE.md:159-160` 注明「不带 prompt 的 argentgo 会读 stdin 然后退出，不会自动打开 TUI」；README.md:68-78 快速开始直接假设已配 key；login/doctor 命令已存在，错误提示会引导 login，但无首次运行检测标记；`cli_hints` opt-in 键仅在 `config/optin_config.go:40` 注册、OPT_INS.md:100 列出，无任何行为消费点。
- **问题**：新用户按 README 装完，无参数运行读到空 stdin 立即退出，屏幕一闪而过且无「先 login / 用 tui / 跑 doctor」指引；#92 修好安装验证后下一步仍卡首次启动。
- **建议**：在 `cli/cli.go` 无参数分支加一次性引导：stdin 为字符设备（os.Stdin.Stat() 检查 ModeCharDevice，零依赖）且无配置/无凭据时，向 IO.Err 打印三行（login 示例、tui/gui 入口、doctor 自检），并在 storage 目录写 onboarded 标记；复用 cli_hints 键作开关（默认关保持逐字节不变）；USER_GUIDE §3 与 OPT_INS.md 同步。
- **验收**：`internal/cli/first_run_guide_test.go`：临时 StorageDir + 空 stdin 两例——无配置输出含 login/tui/doctor，预置带 key 配置时逐字节一致；不设 ARGENT_OPT_INS=cli_hints 时按原行为退出。

---

## 八、性能 / 资源 / 可观测（94–100）

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

### 100. 无可观测性/体积/启动耗时门禁：无 pprof、无 GC 调优、dev 构建未裁剪 `[P2/M]`
- **现状**：全仓 `grep -rniE 'net/http/pprof|runtime/pprof|debug.SetGCPercent|GOGC|SetMemoryLimit'` 无命中（仅测试 2 处 runtime.GC）；`cmd/argent/main.go:11-34` 直接 cli.RunWith 无诊断开关；release 用 `-trimpath -ldflags '-s -w -H=windowsgui'`（build-release.ps1:135,142）但 AGENTS.md 日常命令 `go build -o argentgo.exe ./cmd/argent` 与 ci.yml:49-52 均无裁剪标志（观测到的 21.6M 即未裁剪口径）；CI 无体积或启动时间断言；8 个 init() 均轻量，缺口在「无测量手段」而非已有热点。
- **问题**：出现内存/CPU 回归（如 #94/#95）时无法在线抓 profile；GC 与内存上限无法按部署场景调节；体积/启动时间无基线，回归不可发现。
- **建议**：① main.go 加 env 门控 ARGENT_PPROF_ADDR（非空时手动注册 net/http/pprof 并起 loopback listener，默认关），同时支持 ARGENT_GOGC（debug.SetGCPercent）与 ARGENT_MEM_LIMIT（debug.SetMemoryLimit）；② AGENTS.md dev 构建命令改 `go build -trimpath -ldflags '-s -w' -o argentgo.exe ./cmd/argent`；③ CI 加体积门禁（构建后字节数超 22M 失败）与启动耗时 smoke（`--version` 计时上限，仅报警不精确基准）。
- **验收**：`ARGENT_PPROF_ADDR=127.0.0.1:6060` 启动后 curl `/debug/pprof/heap` 返回 200；不设置时 go build 输出字节低于当前 21.6M；`grep -rn 'pprof' cmd internal --include='*.go'` 有生产命中；go vet/gofmt/test 全绿。

---

## 附：优先处理建议

1. **先清 P0（11 条）**：其中 #1/#2/#15/#39/#74/#75/#76/#77/#94 都是已验证的实锤，单条工作量多为 S，建议集中一个 round 全部修掉。
2. **「未接线原语」是最大宗**：约 40% 的条目属于「纯函数+单测齐全、只差接线」（#3–#8、#16–#18、#29–#34、#40–#43、#54–#57、#62–#68、#73 等），收益/成本比高于新增功能，且大多可用 opt-in 保持逐字节不变。
3. **发版前必须处理**：#74–#93 的版本/文档/门禁一致性问题会直接阻塞 v0.8.2 发版（尤其 #75 的 40w/50w 双口径未决、#77 的安全审计脚本一接 CI 就全红）。
4. **验证方式**：每条的「验收」栏都给了可执行命令；涉及默认路径的改动一律先跑 `go test -p 4 ./... -count=1` 与逐字节 golden，再跑发版门禁 `-race`/`GOOS=linux go build ./...`。
