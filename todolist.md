# argentgo 优化 TODO（仅未做项）

> **生成方式**：2026-11-23，8 个只读子代理并行深挖 git tracked 源码——3 个首轮（OpenHands / dsh / kilocode）+ 5 个二轮遗漏复查（codex / opencode / oh-my-pi / pi-mono / cline / crush / killi）。每条均带 `路径:行号` 源码证据，并双向核对 argentgo 现状后交叉去重；所有代理均未改动业务代码。
>
> **默认化原则（2026-11-23 用户指示，覆盖旧的「opt-in 优先」约束）**：凡改善体验的新功能一律**默认开启**，参数由项目给出**最优默认值**，用户零选择；环境变量仅保留为紧急回退（如 `=0`），不写进用户文档让人觉得「得先配才能用」。旧的 `AGENTS.md` 中「新能力 opt-in 默认关闭、默认路径逐字节不变」条款由本原则覆盖，落地本清单时同步修订 `AGENTS.md` 与 `docs/OPT_INS.md`。现有 74 个 opt-in 属历史债务，按文末「参数归位」分批默认化，不塞进新功能开发。
>
> **证据口径**：每条附 `路径:行号 / 函数名`，区分「已实现但未接线 / 仅桩 / 边界 bug / 缺测试 / 缺验证」。路径相对 `D:\code\mpi\argentgo\`，竞品路径相对 `D:\code\mpi\<项目>\`。
>
> **硬约束**：核心 stdlib-only、零新增 Go 模块、无 cgo、单二进制、自研 shell 不外包；`native/` 是唯一 MyGo 例外。新能力默认开启、参数取最优值，`=0` 回退仅作兼容性兜底。
>
> **优先级**：P0 = 实锤 bug / 安全绕过 / 数据丢失；P1 = 明显体验缺口 / 已有原语未接线；P2 = 改进与防御纵深。工作量 S/M/L。

**统计**：56 条未做项；原编号已清空，本轮重新连续编号。历史 opt-in 默认化见文末「参数归位」。

---

## 一、上下文管理与可恢复性（1–16）

### 1. 压缩摘要追加后重跑「是否真变小」门禁 `[P1/S]`（学自 dsh）
- **竞品证据**：`packages/compaction/compaction-basic/src/region.ts:411-423` 的 `summarizeCompaction` 在模型摘要返回后算 `framedSummaryTokenCount`，`>= shadowedRouteTokenCount` 就拒绝本次压缩（"summary is not smaller than the shadowed content"），把防缩减退化作为 fail-closed 条件。
- **argentgo 现状**：`internal/app/compact.go:359-372` 的缩减门禁发生在 `:384-386` 的 `applyModelSummaryWireWithLocalTrim` **之前**；`internal/app/model_summary.go:220-250` 的 `ApplyModelSummary` 把叙事段追加到已检过的摘要上，随后直接落记录，增补后的大小无人复核。
- **问题**：`model_summary` 下追加的 `## Model narrative`/`## Model next steps` 可把摘要撑回原体积，导致「缓存失效 + 原文丢失」却仍提交压缩。
- **建议**：默认开启二次校验：写回 `out[0]` 后用 `agent.EstimateMessages` 再比一次；若不再小于原视图，剥掉模型叙事段（保留权威抽取式）或放弃压缩并发 `EventNotice`。仅 `ARGENT_COMPACTION_POST_SHRINK_GUARD=0` 时关闭这条重算。
- **验收**：`go test ./internal/app -run TestPostModelSummaryShrinkGuard -count=1`（含「增补后变大必须回落」表驱动用例）。

### 2. 压缩补全调用复用请求前缀以保住 KV 缓存 `[P1/S]`（学自 dsh）
- **竞品证据**：`packages/compaction/compaction-basic/src/summarizer.ts:35-70` 的 `COMPACTION_INSTRUCTION` 作为最后一条 user 追加在回放对话后；`packages/compaction/compaction/src/region.ts:545-564` 的 `buildSummarizationInput` 从 system/消息头与 `requestHeader().tools` 重建与上次请求逐字一致的前缀，使辅助摘要调用命中 warm prefix cache。
- **argentgo 现状**：`internal/app/model_summary.go:98-112` 的 `BuildSummaryPrompt` 拼独立 system + 一条 user；`:263-280` 以 `chatter.Chat(ctx, model, messages, nil, ...)` 发出——工具参数为 nil，完全不回放系统提示/工具表/历史。源码零命中前缀复用。
- **问题**：每次压缩重发完整 system 与工具定义且缓存全失效，长会话压缩成本与延迟显著偏高；模型只看二手摘要，叙事连贯性受限。
- **建议**：默认开启前缀复用：压缩前从现有 `messages` 快照取系统消息 + 原请求工具表 + 被折叠区消息，压缩指令作为末条 user；工具表与消息用现有 `llm.Message`/工具 spec 传递；上限取 200 条 / 32 KiB，失败回落原 `BuildSummaryPrompt`。
- **验收**：`go test ./internal/app -run TestCompactionPrefixReuse -count=1`。

### 3. 压缩请求自身超限时剥离并在分块后递归归约 `[P1/L]`（学自 kilocode）
- **竞品证据**：`packages/opencode/src/kilocode/session/compaction-payload-recovery.ts:12` 的 `matches` 用 `/request entity too large|function_payload_too_large/i` 命中「压缩请求本身」超限，`:29` `strip` 把已完成工具输出打 `time.compacted` 标记、media 附件换 `[Attached <mime>: ...]`；`compaction-chunks.ts:187` `split` 按 0.6×可用窗口切块，`:360 summarize` 以 2048 输出上限逐块摘要，`:389 reduce` 递归两两归并（depth≤3、并发≤3）。
- **argentgo 现状**：`internal/agent/compact_retry.go:65` 只在模型请求报 context overflow 时压缩一次并重放；`internal/agent/compact_drain.go:18` 逐条淘汰；源码零命中「压缩器自身请求超限」恢复路径。
- **问题**：超长会话一旦压缩请求本身撞上体积/上下文上限，压缩永远失败、overflow 反复触发，用户只能丢弃整个会话。
- **建议**：默认开启该恢复链：匹配上述错误模式 → 先剥离已完成工具输出与 media 再重试；仍失败则按 rune 预算切块、用 secondary-model 逐块摘要（上限 2048）并递归归约（depth≤3、并发≤3）。`files/failures/nextSteps` 仍走权威字段，模型只补叙事。
- **验收**：`go test ./internal/agent ./internal/session -run 'TestCompactionPayloadStrip|TestChunkedCompactionReduce' -count=1`。

### 4. agentic 压缩：只折叠上次摘要之后的新消息 `[P2/M]`（学自 cline）
- **竞品证据**：`cline/sdk/packages/core/src/extensions/context/agentic-compaction.ts:52` 的 `runAgenticCompaction` 先用 `:75` 找最新摘要下标，只折叠其后的新消息（`:81-84 slice(latestSummaryIndex+1)`）；`compaction-shared.ts:343 findLatestSummaryIndex` 与 `:368 findCutIndex` 保证切点不拆工具调用/结果对、不吞最近 typed user turn。
- **argentgo 现状**：`internal/app/model_summary.go` 的 `model_summary` 只往抽取式摘要追加叙事，模型看不到原始对话、不做折叠边界判定；`internal/session/compact.go:117` 的 `Summarize(entries[:start], ...)` 仍重扫全部被丢弃条目。
- **问题**：二次及以后压缩把已摘要内容反复喂给模型（成本与叙述漂移）；切点落在 tool_result-only 消息会产生孤儿结果被 provider 拒绝。
- **建议**：默认作为 `model_summary` 的增强：记录最新摘要记录下标，模型输入只取其后消息；切点只落在 assistant 或 typed user 边界；复用已有投影预算控制摘要输入上限；失败回落现有抽取路径。
- **验收**：`go test ./internal/app -run 'TestAgenticFoldOnlyNewSinceSummary|TestFoldCutKeepsToolPairs' -count=1`。

### 5. 生成式压缩摘要的模板校验 + NUDGE 单次重试 `[P2/S]`（学自 opencode）
- **竞品证据**：`packages/core/src/session/compaction.ts:244-245` 定义 `NUDGE`；`:475-477 filled()` 检查回复是否含 `SUMMARY_TEMPLATE` 的 `##` 标题；`:487-512 send()` 第一次回复未填模板时再发一次带 NUDGE 的请求，仍不合格才 `compaction.failed`。
- **argentgo 现状**：`internal/app/model_summary.go:263-300` 的 `GenerateModelSummaryWithUsage` 只发一次 `chatter.Chat`，`:286-289` `ParseModelSummary` 失败即返回 error、调用方回落抽取式。
- **问题**：模型偶发忘记 JSON 键或输出半截；当前直接丢弃已付费调用并回落到信息密度更低的抽取式，本可用一次廉价重试避免。
- **建议**：默认在解析失败且 ctx 仍活时追加一句精简提醒再调一次（最多 1 次），仍失败才回落；两次都走 `EventUsage` 计费。
- **验收**：`go test ./internal/app -run TestModelSummaryNudgeRetry -count=1`。

### 6. 旧摘要 detail 无界膨胀守卫 `[P2/S]`（学自 opencode）
- **竞品证据**：`packages/core/src/session/compaction.ts:226-241` 的 `buildPrompt(update, legacy)` 处理早期模板的 checkpoint 永久携带每个文件/逐次 edit changelog/复述的 AGENTS.md，检测到 `LEGACY_HEADING` 时用 legacy 提示词一次性重写为新详略级别，并声明「新历史优先，过时信息可删」。
- **argentgo 现状**：`internal/session/summary_sections.go:58-77` 的 `PreserveSections` 把原摘要的 Goal/Tool activity 两段整段原样合并回 rebuilt；无 legacy 检测、无体积上界、无「新历史覆盖旧章节」的收敛规则。
- **问题**：多轮压缩中早期冗余细节像滚雪球永久保留，摘要逐渐挤占真正有用的近期上下文，与保住关键章节的初衷相反。
- **建议**：默认在 `PreserveSections` 合并前对 orig 两段做有界裁剪（每段 2048 rune 上限 + 检测早期格式化标记时按新模板重写一次），遵循「新历史优先」替换语义。
- **验收**：`go test ./internal/session -run TestPreserveSectionsBloatGuard -count=1`。

### 7. 压缩模型调用的费用即时入账（含失败/中断） `[P2/M]`（学自 opencode）
- **竞品证据**：`packages/core/src/session/compaction.ts:877-896` 的 `spent` map / `addSpent` / `spend` 在每次模型调用完成即累加，注释明说 "failed and interrupted compactions are billed too"；`:423-426` 后台摘要费用换入时累加进本次压缩统计。
- **argentgo 现状**：`docs/OPT_INS.md:211` 明确记载 `model_summary` 的补全调用「通过 EventUsage 在事件流可见，但不计入 agent 预算与 session stats」；`internal/app/model_summary.go:291-300` 已返回 `llm.Usage` 却无人入账。
- **问题**：长任务反复压缩时真实 token 成本被系统性低估，预算/stats/`--tokens` 与事件流对不上，无法据此做成本控制或评测对照。
- **建议**：默认消费已有 `EventUsage`，把压缩来源 usage 累加进 session stats（失败路径也累加），不改 agent 接口。
- **验收**：`go test ./internal/app -run TestCompactionUsageCounted -count=1`。

### 8. 提供方原生压缩（native compaction：endpoint / trigger） `[P2/L]`（学自 opencode）
- **竞品证据**：`packages/core/src/session/compaction.ts:555-593` 的 `compactNatively`：`:576` 用 `ai.canCompact(request,{mechanism:"trigger"})` 走 trigger（服务端挑保留窗口 + `recentUserMessages`），否则 `:584` 走 endpoint 拿 `response.replacement`；`:564-573` 把替换窗口作为 `native.messages` 原文保存（加密窗口只在同一模型回放），`:311/324/418` 在切换/不可用时回退文本摘要。
- **argentgo 现状**：`internal/session/compact.go` 对 `native|KeepID|transcript` 源码零命中；压缩只有抽取式与 opt-in 生成式，`internal/llm` 三协议无 `compact`/native 窗口类型。
- **问题**：对支持服务端压缩的提供方，本地摘要重写缓存前缀、丢失提供方侧加密状态，长会话恢复质量与缓存命中低于原生方案。
- **建议**：复用现有三协议 `Chatter` transport 增加可选 `Compact`（`stream:false` + 提供方压缩字段）；响应窗口随 `Message`（`omitempty`）保存且仅在原模型回放，其他情况静默回退文本压缩；不新增 Go 模块/服务。对不支持的提供方自动降级，用户无感。
- **验收**：`go test ./internal/llm -run 'TestNativeCompactTrigger|TestNativeCompactFallsBackText' -count=1` + `go test ./internal/session -run TestNativeWindowReplay -count=1`。

### 9. 把 max_tokens 拟合进上下文窗口，避免「提示已满」时的 400 `[P1/M]`（学自 oh-my-pi）
- **竞品证据**：`oh-my-pi/packages/agent/src/output-budget.ts:81` 的 `fitOutputTokensToContextWindow` 在请求前把输出上限压到 `contextWindow − countPromptTokens(context) − 64`（下限 1024）；`countPromptTokens`（`:133`）用最新可信 assistant turn 的提供方 `usage` 作锚点、只本地计数其后追加的尾部，本地计数额外加 1/10 tokenizer 误差余量与每消息 16 framing token；`countFraming`（`:113`）用 `WeakMap` 按数组身份缓存 system/工具定义计数。
- **argentgo 现状**：`internal/llm/anthropic.go:309` 的 `maxTokens()` 与 `internal/llm/client.go` 各请求一律发送固定 8192；`internal/app/context_calibration_test.go` 只把原生 usage 锚点用于压缩阈值判定，未用于输出上限。grep `fitOutput` 全仓零命中。
- **问题**：输出 cap ~384k / 窗口 ~1M 的端点，提示超过 `窗口−cap` 后每个请求都被 400 拒，远早于压缩触发；旁路请求无溢出恢复。
- **建议**：`internal/llm` 加纯函数 `FitOutputTokens(model, msgs, maxTokens, estimate)`，复用已有 native usage anchor 与 `internal/tokenize` 估算；默认应用于新请求（下限 1024、余量 64、误差系数 1.1、每消息 16 framing）；Anthropic 协议（宿主在窗口处停生成）与 cap 已足够时短路。
- **验收**：`go test ./internal/llm -run TestFitOutputTokens -count=1`（假 provider 断言近满窗口时 `max_tokens` 缩小）。

### 10. shake：把大段 fenced/XML 块从活动上下文机械剥离 `[P1/M]`（学自 oh-my-pi）
- **竞品证据**：`oh-my-pi/packages/agent/src/compaction/shake.ts:151` 的 `scanTextForBlockRanges` 逐行扫描 ``` / ~~~ 围栏与顶层成对 XML 标签（围栏内抑制 XML、未闭合不出范围）；`:310 collectShakeRegions` 从最新 `protectTokens` 窗口之外收集这些块与工具结果文本、按 `minSavings` 门槛过滤；`:431 applyShakeRegion` 原地替换为占位、保留所有非文本 part，并在 assistant 消息上累计 `elidedOutputTokens` 使被剥离文本的计费从重放计数中扣除。
- **argentgo 现状**：`internal/agent/trim.go:13-35` 只回收工具结果（`supersededStub` 等），`ARGENT_TOOL_PRUNE` 也只作用于 `Role=="tool"`；全仓无对 assistant/user 文本中 fenced/XML 块的剥离。
- **问题**：一次几百行代码 dump 或大 XML 粘在 assistant/user 消息里，一直占住上下文和缓存前缀直到整轮压缩，期间每轮都为它付费。
- **建议**：`internal/agent` 加纯函数 `ShakeBlocks(messages, protectRunes, minRunes)`（stdlib 逐行状态机，复用 `internal/tokenize`），默认在轮次边界应用：只替换文本块、保留图 part 与工具调用配对、按 rune 计数避免切坏 CJK；保护窗口 4096 rune、最小收益 512 rune。
- **验收**：`go test ./internal/agent -run TestShakeBlocks -count=1`（含 >阈值 ``` 块被替换、非文本 part 保留、短块不变）。

### 11. 嵌套指令的「整段包含」去重，避免重复注入父级规则 `[P2/S]`（学自 oh-my-pi）
- **竞品证据**：`oh-my-pi/packages/coding-agent/src/system-prompt.ts:354` 的 `dedupeContainedContextFiles` 对多个深度的上下文文件按 depth 降序排（离 cwd 越近越权威、放最后），把每个文件规范化为段落块序列；若某文件段落序列被更靠近 cwd 的文件作为连续子序列完整包含则丢弃前者——要求规范化后精确包含、不做模糊匹配。
- **argentgo 现状**：`internal/memory/nested.go` 与 `boundary.go:113-224` 从根到 cwd 收集 AGENTS.override.md/AGENTS.md/CLAUDE.md 并按每文件 token 预算截断，但无「父文件内容已被子文件逐段包含则不再注入」判断。
- **问题**：嵌套仓库里父级 AGENTS.md 的规则常被子目录 AGENTS.md 原样抄一遍，系统提示为同一内容付两次 token 并稀释注意力。
- **建议**：`internal/memory` 加纯函数 `DedupeContainedDocs([]Doc)`（只比规范化段落序列，全 stdlib），默认在预算截断之前跑。
- **验收**：`go test ./internal/memory -run TestDedupeContainedDocs -count=1`（父被子完整包含→只留子；改写/交错→两者都留）。

### 12. 遗弃分支摘要携带机械提取的 readFiles/modifiedFiles `[P2/M]`（学自 oh-my-pi）
- **竞品证据**：`oh-my-pi/packages/coding-agent/src/compaction/branch-summarization.ts:245` 的 `prepareBranchEntries` 第一遍从**全部**条目（即使超预算）提取文件操作，并把既有 `branch_summary` 的 `details.readFiles/modifiedFiles` 并入（`:257-268`）实现跨嵌套摘要累计；`:399 computeFileLists` + `upsertFileOperations` 把去重清单写回摘要文本。
- **argentgo 现状**：`internal/session/branch_summary.go:48` 的 `SummarizeAbandonedBranch` 复用 `Summarize` 生成纯文本、不调 LLM，没有 readFiles/modifiedFiles 字段，也不从 `edit`/`write`/`read` 参数机械抽路径。
- **问题**：`sessions checkout` 切走后，模型只知道遗弃路径上「做了什么」的文字描述，不知道具体碰过哪些文件，可能盲目回退编辑或重复读取。
- **建议**：默认在遗弃分支记录里按工具调用参数（`path`/`file_path`）机械抽取并去重被读/被改文件（无需 LLM、无需新依赖），作为 `[read: ...] [modified: ...]` 行附到摘要，累计合并已有摘要清单。
- **验收**：`go test ./internal/session -run TestBranchSummaryFiles -count=1`。

### 13. 续接摘要保留验证 caveat 与状态区分 `[P2/S]`（学自 codex）
- **竞品证据**：`codex-rs/context-fragments/src/recap_prompt.rs:10-21` 的 `PROMPT_PREFIX` 要求 40-60 词（最多 80）、显式保留 "unresolved availability or validation caveats"（未安装/未部署/未验证）、"Distinguish proposed, queued, implemented, tested, published, and installed work"，并强调 "A new user request establishes scope, not evidence that it has been fulfilled"；`:37-42` 按 4 字节/token 的 8192 token 总预算并用 `floor_char_boundary` 截历史。
- **argentgo 现状**：`internal/app/continuation_brief.go` 基于 `Compaction.Structured()` 生成 goal/files/failures/nextSteps，但没有固定要求保留「未验证/未部署」caveat，也没有状态区分措辞。
- **问题**：resume 后模型把「改了代码」当成「已测试已上线」，或把用户新请求当成已完成的证据，导致错误汇报、跳过必要验证。
- **建议**：默认在续接摘要生成提示里追加上述固定约束段（有界、按 i18n 选中文/英文），要求保留 caveat 与状态区分；不改 `Compaction.Structured()` 权威字段，生成失败回落原文。
- **验收**：`go test ./internal/app -run TestContinuationBriefCaveat -count=1`（脚本化 provider 断言提示含 caveat 约束且摘要保留「未运行验证」字段）。

### 14. 工作区规则文件在会话内变更时重注入 `[P1/M]`（学自 dsh）
- **竞品证据**：`packages/context/agent-instructions/src/index.ts:109-242` 的 `compose` 对每个 agent 维护 baseline 身份，`reconcileInstructionContext`（`:207-222`）在触达路径变化时重新探测指令文件；`state.ts:168-193` 的 `baselineInstructionState` 按 scope 记录 `instructionContentSha1`；`render.ts:257-270` 渲染 `Updated instructions from: <path>` / `Instructions removed: <path>` 的 user 消息并注入下一请求。
- **argentgo 现状**：`internal/app/app_prompt.go:289-294` 只在构建 system 提示时调用一次 `memory.LoadWithBoundary` + `InstructionsToDocs`；全仓对 `Updated instructions`/指令内容摘要零命中，没有运行中重读与重注入通道。
- **问题**：用户在长会话中途编辑 `AGENTS.md` 纠正规则，agent 直到重启都按旧规则工作——这正是用户最期望「立刻生效」的纠错路径。
- **建议**：默认在轮次边界对当前加载的指令文件算 SHA-256 并与上次注入的摘要比对；变化时经既有 steering 通道在下次请求前注入有界的 `Updated instructions from: <path>\n<新内容>` 或移除通知，注入前复用现有提示注入扫描/脱敏；无变化零开销。
- **验收**：`go test ./internal/app -run TestInstructionReconcile -count=1` + `go test ./internal/memory -run TestInstructionDigest -count=1`。

### 15. 图像输入超限时的「按最旧优先卸载」渐进恢复 `[P2/M]`（学自 dsh）
- **竞品证据**：`packages/compaction/compaction-image-offload/src/image-offload.ts:16-46` 的 `offloadOldestImages` 按模型请求顺序扫描 user/tool 消息、跳过 assistant 输出，逐条挑选最旧的未卸载 image occurrence 写入 `image/offload` 事件；`projection.ts:40-73` 以严格校验的目标（seq + 严格递增索引，且必须是当前 surface 节点）在任何请求投影时永久省略这些图像 occurrence，消息节点身份不变，可反复渐进卸载。
- **argentgo 现状**：`internal/llm/image_input_wire.go` 与 `internal/app/app_execute.go:712-734` 只在首条消息注入并限制单图 20 MiB；`ARGENT_MULTIMODAL_TRIM` 只把图像计入 token 估算；对 `offload` 零命中——多轮累积图片一旦触发 provider 图像数/体积拒绝，没有渐进降级路径。
- **问题**：多轮贴多张图后请求带着所有历史图片，provider 报「too many images / payload too large」时现有重试/压缩都无法减少图片数，会话卡死。
- **建议**：默认在会话记录中标记要省略的最旧图像 occurrence（严格按请求顺序、跳过 assistant、只标 user/tool），下一轮 `Messages()` 投影把这些 image part 替换为有界 `[image omitted]` 文本占位（保留 retrievability 提示）；由图像类溢出错误触发、每次卸载最小数量后重试。
- **验收**：`go test ./internal/session -run TestImageOffloadProjection -count=1` + `go test ./internal/agent -run TestImageOverflowRecovery -count=1`。

### 16. 用图像像素尺寸估算视觉 token，替代固定上限 `[P2/S]`（学自 oh-my-pi）
- **竞品证据**：`oh-my-pi/packages/agent/src/image-tokens.ts:61` 的 `base64ImageSize` 只解码 base64 前 64 KiB 头读 JPEG SOF / PNG / GIF / WebP 宽高，`:47 estimateImageTokens` 按尺寸规则计价；尺寸未知时按规则里最贵的 `UNKNOWN_SIZE=65535×65535` 收费，宁高估不低估，本地计数与远端压缩探针用同一规则避免口径漂移。
- **argentgo 现状**：`internal/agent/trim_multimodal.go:25-28` 在 `ARGENT_MULTIMODAL_TRIM=1` 时对图像只按 `detail` 取固定 85(low)/1000(high)，完全不看尺寸。
- **问题**：100×100 截图和 4000×3000 大图都算 1000 token——要么系统性高估提前触发压缩，要么低估错过压缩时机使上下文校准失真。
- **建议**：作为 `ARGENT_MULTIMODAL_TRIM` 的默认增强，用 stdlib `image.DecodeConfig` 或魔数头解析读宽高，按分块公式计价并保留 85/1000 作为上下界；解码失败/非 data URL 回退当前固定值；零新增依赖、无 cgo。
- **验收**：`go test ./internal/agent -run TestImageDimensionTokens -count=1`（同 detail 的 100×100 与 2000×2000 PNG 得到不同估算，坏 base64 回退旧值）。

---

## 二、安全边界（17–30）

### 17. 非幂等工具调用的持久化领取账本 `[P0/S]`（学自 OpenHands）
- **竞品证据**：`src/utils/claim-client-tool-call.ts:10-33` 的 `claimClientToolCall(ledgerKey, toolCallId)` 做事前先把 toolCallId 写入按会话命名的账本，已领取直接返回 false；账本损坏则从头开始、写失败「接受重放风险也不阻断调用」。调用点 `src/services/secret-request.ts:29-37` 与 `child-conversation-launch.ts:519-528` 说明重放会重复提问或启动第二个计费会话。
- **argentgo 现状**：`internal/agent/guard.go:51` 的 `newDedupe` 仅做同一次运行内按规范化参数去重；`internal/agent/byte_replay_test.go` 只保证字节回放，无跨会话副作用去重。
- **问题**：resume、流中断回补、SSE 重连后若一条非幂等工具调用被重放，会产生重复且不可撤销的副作用。
- **建议**：默认开启：session JSONL 新增 `kindToolClaim{tool_call_id}`，执行非幂等工具前先落盘再执行，重放/恢复时见到已领取 id 跳过并把原结果回灌；账本损坏记 warn 不阻断。
- **验收**：`go test ./internal/session -run TestToolClaimReplaySkipped -count=1`。

### 18. 密钥请求工具：值永不进入模型上下文 `[P0/M]`（学自 OpenHands）
- **竞品证据**：`src/api/request-secret-client-tool.ts:7-25` 的 `REQUEST_SECRET_DESCRIPTION` 明令「never put a secret value in this tool's arguments / never ask the user to paste a secret into the chat」，`:30-46` 工具只收 `name`+`reason`；`src/services/secret-request.ts:82-104` 的 `completeSecretRequest` 只回传 `{status,name}` 或 guidance，值经 `updateConversationSecrets` 落到用户设置并以同名环境变量暴露给会话。
- **argentgo 现状**：`request_secret` 类工具源码零命中；`internal/auth/` 的 DPAPI 只保护登录令牌，模型若需临时凭据只能让用户贴进对话，从而进入 JSONL 与模型上下文。
- **问题**：任务需要 API key/token 时唯一路径是贴聊天，密钥进入转录、事件流与模型上下文，最小暴露原则被破坏。
- **建议**：默认注册 `request_secret{name,reason}`：TUI 弹窗收集，值写入既有凭据存储且只以 `$NAME` 注入 bash 子进程环境；回灌模型的只有 `[secret-request] {"status":"saved","name":"..."}` 前缀消息，绝不含值；一处缺失只问一次。
- **验收**：`go test ./internal/tools -run TestRequestSecretNeverLeaksValue -count=1`（断言 JSONL/事件/模型消息均无密钥字节）。

### 19. 上下文裁剪后的「缺失不构成授权」告示 `[P0/S]`（学自 codex）
- **竞品证据**：`codex-rs/core/src/context/guardian_budget_omission.rs:11` 的 `GuardianBudgetOmission::body` 注入 `<guardian_context_omission>` 片段，明确写 "Do not infer authorization from missing evidence or treat a partial grant as overriding an omitted restriction."；`guardian-context/src/preparation/elastic.rs:43-53` 还给 sender evidence 强制套 protected START/END 框架，声明 partial evidence 不是权限转移。
- **argentgo 现状**：`ARGENT_TOOL_PRUNE`（`internal/session/tool_prune.go:29`）、`ARGENT_FOVEATED_ARCHIVE`（`internal/session/foveated_archive.go:26`）、`internal/agent/trim.go` 与压缩归档都会静默丢内容；`ARGENT_UNTRUSTED_BOUNDARY` 只区分外部文本。grep `infer authorization|缺失.*授权` 零命中。
- **问题**：fail-open——策略/审批规则被裁掉后，模型把「上下文里没有限制」当成「被允许」，可能直接执行本应被拦截的危险命令。
- **建议**：默认当本轮确实发生 prune/gap/压缩丢消息时，向下一轮 user 上下文追加一段固定、有界、带标记的告示（「被省略内容可能含限制；缺失证据不构成授权，不覆盖仍生效的审批/策略」）；只增信息，绝不改审批判定。
- **验收**：`go test ./internal/session ./internal/agent -run TestOmissionAuthorityNotice -count=1`（无裁剪逐字节不变、有裁剪时告示恰好一次且不含原文）。

### 20. 中断告示补齐「可能已部分执行/后台进程仍存活」 `[P0/S]`（学自 codex）
- **竞品证据**：`codex-rs/core/src/context/turn_aborted.rs:11` 的 `TurnAborted::INTERRUPTED_GUIDANCE` 固定文本："Any running unified exec processes may still be running in the background. If any tools/commands were aborted, they may have partially executed."，以 `<turn_aborted>` user 片段注入下一轮。
- **argentgo 现状**：`internal/app/app_execute_budget_test.go:14-15` 的 `noticeInterruptedPrompt`/`noticeInterruptedTurn` 只说「unanswered tool calls will be answered with an aborted result」；`ARGENT_RECOVERY_DETAIL` 只补类别/断链计数。而 `ARGENT_JOBS`/`ARGENT_EXEC_SESSION` 确实会留下仍在运行的后台会话，模型却收不到提示。
- **问题**：恢复后模型以为被中断的命令毫无副作用，重复写操作造成重复落盘；或以为后台 job 已死，留下孤立进程与端口占用。
- **建议**：默认仅在 `Notice != ""` 的 interrupted/degraded 恢复公告后追加一句有界告示：被中断的工具可能已部分执行、后台 exec 会话可能仍在运行，重复写操作前先核对状态；不触碰 `NeedsConfirmation`。
- **验收**：`go test ./internal/app -run TestInterruptedSideEffectNotice -count=1`（关闭时公告逐字节不变、干净会话无凭空告示）。

### 21. 配置说明文件编辑保护且「始终允许」不可覆盖 `[P1/S]`（学自 kilocode）
- **竞品证据**：`packages/opencode/src/kilocode/permission/config-paths.ts:11` 的 `CONFIG_DIRS=[".kilo/",".kilocode/"]`、`:23 CONFIG_ROOT_FILES` 含 `AGENTS.md`；`src/permission/index.ts:197-216` 把这类请求强制 `ask` 并禁止持久化，`:236-238` 把 `allow` 降级为 `ask`，`:338-340` 把 `always` 降级为 `once`，`:383 saveAlwaysRules` 直接拒绝写入。
- **argentgo 现状**：`internal/security/protected_paths.go:14` 的 `DefaultProtectedNames` 仅 `.git/.argent/.aws/.ssh`，`AGENTS.md`/`CLAUDE.md`/`config.json` 不在其中；`internal/security/chain.go:138` 的 `ForceAsk` 装配层默认 nil；`approver.go:276 GrantAlways` 按工具名永久放行。
- **问题**：被提示注入的模型可改写 `AGENTS.md`/项目配置，把恶意指令或放宽的策略持久化到下次会话，是跨会话污染而非一次性越权。
- **建议**：默认在 `IsPathProtected` 之外加一层「需询问且不可 always」判定：命中 `AGENTS.md`/`CLAUDE.md`/`config.json(c)`/`.argent/` 时经 `ForceAsk` 走询问且 `always` 只记一次性；纯文本路径匹配、无新依赖。
- **验收**：`go test ./internal/security -run TestConfigFileGuardCannotBeAlways -count=1`。

### 22. 宽泛 read 规则自动收窄：密钥文件改为询问 `[P1/S]`（学自 kilocode）
- **竞品证据**：`packages/opencode/src/kilocode/permission/read.ts:4` 的 `guard` 把路径匹配到 `*.env`/`*.env.*`（`*.env.example` 放行）；`:11 harden` 在权限为 read、原规则 allow 且 `PermissionRule.broad(rule)` 时把命中项改成 ask，使 `read: * allow` 不会静默读到 `.env.local`。
- **argentgo 现状**：`internal/security/chain.go:551` 默认 `DefaultPolicy(".")` 对 read 放行；要拒绝密钥文件须用户自写 `DenyPaths: ["**/.env"]`；没有「宽泛规则命中密钥文件时自动转 ask」的收窄。
- **问题**：默认安装下模型可直接 read `.env`/`.env.local`/`.env.production`，secret 进入上下文、会话 JSONL 与工具输出，泄露后难以回滚。
- **建议**：默认在 `Policy.Evaluate("read", args)` 的 Allow 分支对文件名（大小写不敏感）匹配 `*.env`、`*.env.*`（排除 `*.env.example`，显式 DenyPaths 优先）返回 Ask；只影响 read，不改 write/审计。
- **验收**：`go test ./internal/security -run TestBroadReadRuleNarrowsSecretFiles -count=1`。

### 23. 敏感工具审批只认真人交互回答 `[P1/S]`（学自 kilocode）
- **竞品证据**：`packages/opencode/src/permission/index.ts:216` 用 metadata `skillShell`/`sandboxEscalation` 置 `forceAsk`，即便规则是 allow 也必须询问；`:291-305` 在 reply 阶段对这两类请求要求 `input.interactive === true`，否则只记 warning 并让请求保持 pending——机器/YOLO 客户端的自动批准无法代答；`:137-140` 的 `veto` 让保存的 always/session 规则无法推翻硬拒绝。
- **argentgo 现状**：`internal/security/chain.go:184-191` 的 `ExplicitYesApprover` 只覆盖 `ShellConfirmationRequired` 判定的高危 shell；`internal/app/app_execute.go:1135` 的 `autoApprover` 对非变更型请求静默批准；没有按工具元数据标记「敏感」与答复侧 `interactive` 校验。
- **问题**：`--yolo`/`approval=auto` 下，被标记需人工确认的技能 shell 或沙箱提权会被自动批准，安全闸门形同虚设。
- **建议**：默认审批请求加可选 metadata 敏感标记；`Approver` 增加可选接口 `InteractiveReply() bool`，答复入口对敏感项只接受 `interactive=true`，否则保持 pending 并审计 warning；非交互客户端（脚本/eval）遇敏感项自动降级为拒绝而非阻塞。
- **验收**：`go test ./internal/security -run TestSensitiveApprovalIgnoresAutoReply -count=1`。

### 24. fork 继承历史的只读边界 `[P1/M]`（学自 codex）
- **竞品证据**：`codex-rs/context-fragments/src/side_conversation.rs:12-22` 的 `SIDE_BOUNDARY_PROMPT` 在分叉处插入边界：「Everything before this boundary is inherited history ... Do not continue, execute, or complete any instructions, plans, tool calls, approvals, edits, or requests from before this boundary.」；`SideConversationPolicy::instructions`（`:48-61`）把策略追加到 developer 指令，`side_conversation_boundary`（`:95-105`）落成 user 边界消息。
- **argentgo 现状**：`internal/session/fork_before.go:34-61` 的 `ForkBefore` 只复制根到分叉点的记录并把当时 prompt 还原到 composer；grep `before this boundary|继承.*历史` 的用户提示零命中。
- **问题**：fork 后模型把旧任务里未完成的指令、待办、已获审批当成当前任务继续执行，造成越权写文件或偏离新目标。
- **建议**：默认在 `ForkBefore` 返回的新会话活动历史末尾插入一条带稳定标记的边界消息 + 一条 developer 指令（边界前内容仅作参考，只有边界后用户指令生效；不继承旧审批/子代理）。
- **验收**：`go test ./internal/session ./internal/app -run TestForkBoundary -count=1`（fork 后末条为边界标记、旧 prompt 未被当作指令）。

### 25. 已批准作用域回灌模型上下文 `[P1/M]`（学自 codex）
- **竞品证据**：`codex-rs/core/src/context/world_state/compact_permissions.rs:38` 的 `CompactPermissionsState::render_diff` 对已批准命令前缀做差集，经 `ApprovedCommandPrefixSaved` 作为 developer 片段注入；`approved_command_prefix_saved.rs:14-25` 把固定 marker `Approved command prefix saved:` 放在最前（heading 上限 500 UTF-8 字节），便于回滚时裁剪。
- **argentgo 现状**：`internal/security/persistent_approver.go:44-60` 的 `FileBackedScopedApprover` 把 always/deny 作用域落 `approvals.jsonl`，但只在 `Ask` 里判定；`internal/app/approval_persist.go` 仅负责构造审批器，没有把已批准作用域写进 system/developer 提示的落点。
- **问题**：`APPROVAL_PERSIST` 的授权对模型不可见，跨会话或压缩后模型反复请求同一审批，或误判当前无任何授权，增加交互摩擦与 token 消耗。
- **建议**：默认审批授予后由 chain 回调把 `scopeFor` 得到的工具名/命令前缀写入有界（约 500 字节）提示片段，并随 `Compaction` 保留；只含作用域，绝不写自由文本参数。
- **验收**：`go test ./internal/app ./internal/security -run TestApprovalScopeContextHint -count=1`（前缀进入下一轮 system 提示且原始参数不出现）。

### 26. 嵌套 AGENTS.md 工具后发现提醒 `[P1/S]`（学自 killi）
- **竞品证据**：`crates/killi/src/prompt.rs:283` 的 `Instructions::touched` 在工具调用后从项目根向目标路径逐级探测 `AGENTS.md`/`CLAUDE.md`，用 `known` 集合去重、累积到 `found`，再由 `turn.rs:333` 以 `<system-reminder>These AGENTS.md files apply to paths you just used...Read them before changing anything in those folders` 追加到下一轮；只给文件名不给全文，system prompt 与缓存前缀不动。`dispatch.rs:181` 的 `touched` 对目录路径补探子路径。
- **argentgo 现状**：`internal/memory/nested.go:35` 的 `LoadForDirs(cwd, dirs)` 已具备发现/去重/预算裁剪，但调用者只有测试，生产代码零调用；`ARGENT_RULE_DISCOVERY` 只列 home/root 规则名，不覆盖工具触及的子目录。
- **问题**：多包仓库里模型读了 `src/parser/x.go` 却不知道 `src/parser/AGENTS.md` 的局部约定，容易改错；把全文塞进 system 会破坏缓存前缀，只能学 killi 做末尾提醒。
- **建议**：默认 dispatch 工具完成时收集 read/edit/write/glob/grep 路径与 bash cwd（有界 200 条），请求前 `memory.LoadForDirs(root, dirs)` 取 docs，但只用 `Doc.Path` 生成一条有界 reminder（≤8 路径、≤512 字节），经 `injectSteeringAs(extra...)` 追加；不注入全文、不改 system。
- **验收**：`go test ./internal/memory -run TestLoadForDirsDiscoversNestedAncestorsAndDedupes` + 新增 `TestNestedInstructionHintAppendsReminderOnly`。

### 27. 「始终允许」生成分层 bash 通配作用域 `[P2/S]`（学自 kilocode）
- **竞品证据**：`packages/opencode/src/kilocode/bash-hierarchy.ts:16` 的 `BashHierarchy.addAll` 对命令 token 数组 `["npm","install","lodash"]` 逐级生成 `npm *`、`npm install *`、`npm install lodash`（`:20-27`），并用 `BashArity.prefix` 在 exact text 与 arity 前缀重合时跳过重复项。
- **argentgo 现状**：`internal/security/approver.go:276 ScopedApprover.GrantAlways(name)` 以工具名为键；`internal/app/app_execute.go:485-488` 给 `NewFileBackedScopedApprover` 传 `scopeFor=nil`，即作用域只记工具名。按 arity 生成多级通配源码零命中。
- **问题**：批准 `git status` 后 `git log`、`npm install foo`/`npm install bar` 每次都要重新审批；直接放行整个 `bash` 又过宽，用户被迫在打扰与过度授权间二选一。
- **建议**：默认记录 bash 的 always 作用域时按空白切词（沿用现有自研 shell 词法，不外包），生成前缀通配集合并去重，写入 `FileBackedScopedApprover` 的 scopeFor；只影响持久化 scope 文本。
- **验收**：`go test ./internal/security -run TestBashHierarchicalScopes -count=1`。

### 28. 会话执行状态可见性 `[P1/S]`（学自 killi / argentgo 未接线原语）
- **竞品证据**：`crates/killi/src/live.rs:137` 的 `join` 为每个顶层会话写持 flock 的 presence 文件并监听 socket，`:206 update` 记录 working/idle，`:272 list` 汇总全部会话；`docs/decisions.md:298` 写明请求前追加「同一 worktree 有其他会话在工作」的提醒，避免互相 revert/overwrite/commit。
- **argentgo 现状**：`internal/agent/session_status.go:25` 的 `SessionStatusRegistry`（含 idle/planning/working/done/error 五态）只被测试调用，全仓无生产调用；`internal/server/sessions.go:9` 的 `SessionInfo` 仅 id/title/updated，`session.list` 无状态字段。
- **问题**：`--serve`/多会话场景无法知道哪个会话正在改文件，并发会话容易踩踏；完整状态机白写。
- **建议**：默认 App 持有 registry，`Execute` 入口 `Set(id, working)`、defer 按结果写 `done|error`、退出 `Delete`；`internal/server` 仿 `SessionTreeStore` 加可选 `SessionStatusStore.Snapshot()`，`session.list` 多一个 `status` 字段；`sessions --json` 同步。
- **验收**：`go test ./internal/agent -run TestSessionStatusRegistry` + 新增 `TestSessionListStatusOptIn`。

### 29. 跨进程会话 turn 租约防止并发跑同一会话 `[P2/M]`（学自 kilocode）
- **竞品证据**：`packages/opencode/src/kilocode/session/turn.ts:47` 的 `hold` 写 `<sessionID>.<pid>` 标记（内容为主机名），运行中每 `ttl/3` 刷新 mtime，释放时删除；`:89 foreign` 扫描目录，用 `process.kill(pid,0)` 判断本机 PID 存活，本机标记 24h 未刷新或异机标记 60s 未刷新视为陈旧并清理。
- **argentgo 现状**：`internal/session/append_lock_windows.go:24`/`append_lock_unix.go` 只有追加 JSONL 时的文件锁，串行化写入但不阻止两个进程各自运行 turn；全仓无按 PID 存活/租约过期的 turn 锁。
- **问题**：`--rpc`、原生窗口与 CLI 同时打开同一会话时，两边各自压缩/追加，可能产出交错记录、重复工具结果与丢失更新。
- **建议**：默认在存储目录写 `<sessionID>.<pid>` 标记（主机名 + mtime 刷新），开启 turn 前检测到活标记则拒绝并提示；TTL（本机 24h/异机 60s）+ PID 存活双判定清理陈旧标记，进程退出清理。Windows 复用 `internal/proc` 的存活判定。
- **验收**：`go test ./internal/session -run TestTurnLeaseForeignDetection -count=1`。

### 30. 提供方拒绝/错误占位不回放到模型 `[P1/S]`（学自 oh-my-pi）
- **竞品证据**：`oh-my-pi/packages/agent/src/replay-policy.ts:11` 的 `isProviderRefusalMessage` 判定 `stopReason==="error"` 且 `stopDetails.type ∈ {refusal, sensitive}`；`:34 toProviderReplayMessage` 在回放时整条丢弃这类 assistant 轮，并把 `__synthetic===true && source==="assistant_stop_error"` 的工具结果占位改写成 `PROVIDER_ERROR_NOT_EXECUTED_TEXT`，只保留「未执行」事实、不把提供方的拒绝解释喂回分类器。
- **argentgo 现状**：`internal/llm/client.go:30` 的 `FinishReason` 只是字符串，没有 `stopDetails.type` 解析；`internal/session/` 的 `messageEntries` 与 `agent.Options.SeedContext` 投影路径没有任何拒绝过滤。已有 `refusal_reason` opt-in 只暴露工具错误原因，不是模型回复拒绝。
- **问题**：一次内容策略拒答被 JSONL 持久化后，resume 会把它连同拒绝理由一起重放，使后续每一轮更容易被拒（自我污染）。
- **建议**：三协议解析层把 `stop_reason=refusal|sensitive`（Anthropic）与 `finish_reason=content_filter`（OpenAI 系）记为 `omitempty` 结构化字段；默认在 `messageEntries`/SeedContext 处丢弃该 assistant 轮、把流错误占位改写为固定「未执行」文本。
- **验收**：`go test ./internal/session ./internal/llm -run TestRefusalReplay -count=1`。

---

## 三、工具与执行深度（31–45）

### 31. 前台长 sleep 提前拒绝 `[P0/S]`（学自 killi）
- **竞品证据**：`crates/killi/src/tools/bash.rs:66` 的 `leading_sleep(command)` 解析命令首个 `sleep N`，`:107-110` 在非后台且 `N >= MAX_SLEEP(30)` 时直接返回以 `Not run:` 开头的错误，给出 `task_output` 带 `wait` 或 `until <check>; do sleep 5; done` 两条替代；`docs/decisions.md:230` 记录一个会话里 `sleep 270` 被重复 12 次共 3045 秒。
- **argentgo 现状**：有 bash 超时转后台 job（`internal/bash/tools_builtin_bash.go:74/81`、`internal/jobs/`），但无「执行前识别前台长 sleep 并拒绝」的守卫；`Not run:`/`leading_sleep` 在 `internal/` 零命中。
- **问题**：模型用 `sleep 270; tail log` 空等，直到工具超时（默认 60s）才转后台，白占一轮工具时间与上下文，用户侧表现为卡住。
- **建议**：默认 bash 派发前用自研 shell 已有词法结果判断「首条命令为 sleep 且字面秒数 ≥30 且非后台」，返回 `Not run:` 等价文案并列出两种替代；含管道/`cd` 前缀/变量的复杂形式保守放行。
- **验收**：`go test ./internal/bash -run TestSleepGuard`（覆盖 `sleep 270`、`cd x && sleep 30` 放行、`sleep 5` 放行、后台放行）。

### 32. 软超时 bash 观察语义（仍在运行 + 续读日志 + 结构化元数据） `[P1/S]`（学自 OpenHands）
- **竞品证据**：`src/types/agent-server/core/base/action.ts:32-49` 的 `ExecuteBashAction`：`command` 可为 `""`（view additional logs）或 `"C-c"`（中断当前进程），另有 `is_input`/`timeout`/`reset`；`observation.ts:60-85` 用 `exit_code:-1` 表软超时仍在运行；`common.ts:16-49` 的 `CmdOutputMetadata` 含 exit_code/pid/working_dir/username/hostname 由 PS1 哨兵采集、与 stdout 分离。
- **argentgo 现状**：`internal/bash/exec_session_runner.go:139` 的 `exitCodeFromWait` 把 -1 定义为「被信号终止」而非「仍在运行」；`tools_builtin_bash.go:112` 的 schema 无空命令续读日志；`:182 isSessionInterrupt` 把 `\x03` 映射为 Kill；持久 shell 无 working_dir/pid 结构化回传。
- **问题**：长命令软超时后模型不知道进程还在跑，只能重跑造成重复副作用；`cd` 后模型也无法确知新 cwd，容易拼错相对路径。
- **建议**：默认观察新增 `still_running`/`cwd`/`pid`（从哨兵解析、与 stdout 分离），空 command 续读该会话尾部日志，`C-c` 走既有 `isSessionInterrupt` 但保留会话。
- **验收**：`go test ./internal/bash -run TestSoftTimeoutReadMoreLogs -count=1`。

### 33. 会话内易失工具结果缓存（URI + 空闲淘汰 + 反重复副作用） `[P1/M]`（学自 cline）
- **竞品证据**：`cline/sdk/packages/core/src/session/services/tool-result-cache.ts:17` 的 `ToolResultCache` 用 Map + 字节预算（16 MiB）+ LRU；`:32 store` 把超大结果存为 `cline://cache/<session>/<id>.result.txt`；`:75 advanceIteration` 每轮自增，连续 5 轮未显式 read 即淘汰；`:7` 的 `TOOL_RESULT_CACHE_MISS` 文案明确写 "DO NOT repeat side-effecting actions to recover output"。
- **argentgo 现状**：`ARGENT_OUTPUT_SPILL`（`internal/app/output_spill_wire.go:14-63`）把产物永久落盘到 `<storage>/artifacts` 并靠过期清理，无会话内 URI 方案、无按进度的空闲淘汰、无「勿重放副作用」提示。
- **问题**：大结果要么全量进上下文（后续每轮重复计费），要么落盘后无生命周期约束；产物被清理后模型可能重试有副作用的工具调用来「取回」结果。
- **建议**：默认标准库 map + 字节上限（16 MiB）+ 轮次计数器；超限结果存为 `argent://cache/<id>.result.txt` 并替换工具结果正文，在 `read` 工具 `resolve` 前加 scheme 分支；每轮 `advanceIteration` 淘汰空闲项（5 轮）；miss 返回可读提示且绝不重放副作用。
- **验收**：`go test ./internal/agent -run 'TestToolResultCacheIdleEviction|TestToolResultCacheMissNoSideEffectHint' -count=1`。

### 34. 写入时行尾（EOL）规范化 `[P1/S]`（学自 cline）
- **竞品证据**：`cline/sdk/packages/core/src/extensions/tools/executors/line-endings.ts:13` 的 `normalizeNewFileLineEndings` 在 Windows 上把纯 LF 新建文件转成 CRLF（除非已含 `\r`）；`:31 detectLineEnding` 用「任意位置出现 `\r\n`」判定既有文件 EOL，`:35 normalizeLineEndings` 统一到检测到的 EOL，从而编辑 CRLF 文件不产生混合行尾。
- **argentgo 现状**：`internal/tools/tools_builtin_write_edit.go:58`/`:74`/`:219` 直接 `os.WriteFile(path, final, 0o644)`；全仓 `internal/tools` 零命中 `LineEnding`/`normalizeLineEndings`，写盘前后不做 EOL 检测或转换。
- **问题**：Windows 上模型新建的文件落成 LF（Git 噪声、依赖 CRLF 的工具异常）；编辑 CRLF 文件时插入的 `new` 仍是 LF，造成单行尾混合，diff/校验难以解释。
- **建议**：默认从 `before` 调 `detectLineEnding`，新文件在 `runtime.GOOS=="windows"` 且内容无 `\r` 时转 CRLF；仅对最终落盘字节做规范化，接入 write/edit/apply_patch 三处最终写入点。
- **验收**：`go test ./internal/tools -run 'TestWriteNormalizeNewFileEOL|TestEditPreservesDetectedEOL'`。

### 35. 写文件保留原 UTF-8 BOM `[P2/S]`（学自 opencode）
- **竞品证据**：`packages/core/src/file-write.ts:37` 的 `FileWrite.commit` 契约为 "Write approved text, keeping an existing UTF-8 BOM"，`:83` 调用 `fileMutation.writeTextPreservingBom`。
- **argentgo 现状**：`internal/tools/tools_builtin_write_edit.go:58` 直接 `after := []byte(args.Content)`、`:74 os.WriteFile`；`internal/config/jsonc.go:12` 只在**读**侧 `TrimPrefix("\ufeff")`，写侧零 BOM 处理。
- **问题**：目标平台 Windows，PowerShell 5.x 写出的文件普遍带 BOM；模型整文件 write 后 BOM 被抹掉，下游依赖 BOM 的脚本/PowerShell 解析退化，diff 显示整文件变更。
- **建议**：默认写前读 `before`（该路径已有 `os.ReadFile`），若 `bytes.HasPrefix(before, BOM)` 且 `after` 无 BOM 则前置；空操作判等仍在加 BOM 之后做。
- **验收**：`go test ./internal/tools -run 'TestWritePreservesExistingBOM|TestWriteBOMNoopUnchanged' -count=1`。

### 36. 截断标记报告原始 token 数与总行数 `[P1/S]`（学自 codex）
- **竞品证据**：`codex-rs/utils/output-truncation/src/lib.rs:34` 的 `format_truncated_text` 在片段前加 `Warning: truncated output (original token count: N)\nTotal output lines: M\n\n`；`guardian-context/src/truncation.rs:49` 在 UTF-8 边界头尾之间插 `<truncated omitted_approx_tokens="K" />`，K 是被省内容的近似 token 数（4 字节/token）。
- **argentgo 现状**：`internal/tools/tools_clip.go:38` 的标记不含任何计数；`internal/session/tool_prune.go:67` 报 `N runes`；`internal/tokenize/tokenize.go:20` 已有 CJK/emoji 感知的 `Estimate` 但未接入裁剪标记。
- **问题**：模型看到「runes」无法换算成自身 token 预算，也无法知道原文多少行，容易把残片当成全量继续推理。
- **建议**：默认在 `clipTo`/`PruneToolResults`/`CollapseOutput` 的裁剪帧上用 `tokenize.Estimate(raw)` 与 `strings.Count(raw,"\n")+1` 生成有界头（如 `[truncated: original ~N tokens, M lines, kept head+tail]`）。
- **验收**：`go test ./internal/tools ./internal/session -run 'TestClipLossHeader|TestPruneLossHeader' -count=1`（含中文/emoji 估算单调性）。

### 37. JSON 转义感知的工具输出预算 `[P1/S]`（学自 codex）
- **竞品证据**：`codex-rs/utils/output-truncation/src/lib.rs:73` 的 `truncate_mcp_tool_result` 在序列化后若超 `max_bytes` 进入裁剪循环：按 `preview_budget = preview_budget * max_bytes / truncated_len` 逐轮收缩再试，因为 preview 作为 JSON 字符串会被转义（`/`/`\`/换行各膨胀）；`:22` 的 `with_serialization_allowance` 直接乘 1.2 预留开销。
- **argentgo 现状**：`internal/tools/tools_clip.go:20` 的 `clipTo` 只按原始字节 2/3+1/3 头尾切并插标记，随后 `internal/llm/anthropic.go:136` 等才 `json.Marshal` 整个请求。grep `serialization_allowance` 零命中。
- **问题**：构建日志/MCP 文本含大量引号、反斜杠、换行时，按 N 字节裁剪的输出上线后膨胀到接近 2N，请求体超限被 provider 拒绝，或过早触发压缩。
- **建议**：默认在 `clip()` 返回后对结果做一次 `json.Marshal` 长度探测，按 codex 比例公式最多迭代 4 轮收缩原始预算，失败回落旧帧；同时把 `maxToolOutput` 乘 1.2 系数。
- **验收**：`go test ./internal/tools -run TestClipJSONEscapedBudget -count=1`（全 `\n`/`\"`/`\\` 输出断言 marshal 后长度 ≤ limit）。

### 38. read 工具单行字符上限（defang minified 文件） `[P2/S]`（学自 cline）
- **竞品证据**：`cline/sdk/packages/core/src/extensions/tools/executors/file-read.ts:82` 的 `readTextWindow` 在 `:143-146` 对每行超过 `MAX_LINE_CHARS`（2000）时截为前 2000 字符 + `[line truncated]`，`:188-192` 用 `Showing lines ...` 告知分页。
- **argentgo 现状**：`internal/tools/tools_builtin_read.go` 有 10 MB 文件上限、分页窗口与首尾裁剪 `clip`，但没有单行字符上限：一个 5 MB 的单行 JSON 能通过文件上限，最终只被 `clip` 成无意义的头尾。
- **问题**：minified JS/JSON/生成文件的一行吞掉整个读取预算，模型看不到结构也无法按行定位；`clip` 的头尾片段还会误导后续编辑锚点。
- **建议**：默认在 `readTool.Run` 组装输出前、按 rune 安全截断每行到 2000 并附 `[line truncated]`，随后仍走既有 `clip`；配合工具描述告知模型用分页/搜索展开。
- **验收**：`go test ./internal/tools -run TestReadDefangsMinifiedLine`（10000 字符行输出带截断标记且不超预算，CJK 行不被切坏）。

### 39. edit 失配时指出首条分歧行 `[P2/S]`（学自 killi）
- **竞品证据**：`crates/killi/src/tools/edit.rs:292` 的 `closest_hint` 在失配时找最接近的起始行，对多行 `old_string` 取「最长匹配前缀」（`:310-317`），报告首个真正分歧的行号及该文件该行 ≤160 字符原文（`:325-334`）；`docs/decisions.md:333` 记录 76/961 次 edit 失败，旧提示只报起始行导致模型反复重发同一块。
- **argentgo 现状**：`internal/tools/tools_builtin_write_edit.go:204` 的 `count==0` 分支只返回 `edit: old text not found in %s`；`:772 editRepairError` 虽有 `formatSimilarLines`（"most similar lines"），但只在 repair 阶梯出现，且不是「最长前缀的首条分歧行 + 实际文本」。
- **问题**：长文件多行 edit 失配时模型只能盲读/盲重试，同一路径连续失败。
- **建议**：默认在 `count==0` 分支追加 `; first divergent line N: <file excerpt ≤160 runes>`，复用 `formatSimilarLines` 候选窗口但改为按最长前缀定位分歧点；只改错误文案，不改匹配/替换语义。
- **验收**：`go test ./internal/tools -run 'TestEditMissHint|TestEditRepairError'`（多行后行分歧、单行失配、CJK/超长截断）。

### 40. 中心化工具超时返回结构化错误并在 hooks 前还原信号 `[P2/S]`（学自 dsh）
- **竞品证据**：`packages/guard/timeout-policy/src/index.ts:41-48` 的 `toolTimeoutResult` 当自身截止时间胜出时返回 `isError` 且 `error.code === TOOL_TIMEOUT` 的结构化结果；`:55-80 apply` 包裹 `tools/execute`，用 `deadline` 派生信号、临时替换 `exec.signal`、`finally` 中还原上游信号（使 post-execute 监听器看不到已中止的派生信号），并用 `timeoutOf` 区分「自己的超时」与嵌套外层超时。
- **argentgo 现状**：`internal/tools/tool-timeouts.go:12-18` 的 `toolTimeouts` 表与 `clampTimeout` 把 deadline 下放到每个工具内部；`git_status.go:32-39` 等直接 `fmt.Errorf("... timed out after %s", ...)`。对 `ErrToolTimeout`/`TOOL_TIMEOUT` 全仓零命中——无统一超时错误码。
- **问题**：重试/审计/护栏层无法按「超时」语义路由（只能匹配文案）；嵌套 deadline 下超时原因易被错误归因；PostToolUse hook 观察到的是已取消的 ctx，可能写出误导性审计记录。
- **建议**：默认在 agent dispatch 边界按既有 `toolTimeouts` 表包裹调用，到期返回包装 `ErrToolTimeout`（带 code）的模型可读错误；在调用 PostToolUse/审计前还原原始 ctx（以普通错误而非取消态呈现）。
- **验收**：`go test ./internal/agent -run TestToolTimeoutPolicy -count=1` + `go test ./internal/tools -run TestToolTimeoutSentinel -count=1`。

### 41. 流式发布工具调用参数的增量预览 `[P2/S]`（学自 kilocode）
- **竞品证据**：`packages/opencode/src/kilocode/session/tool-input.ts:14` 的 `delta` 在模型仍在写工具参数时每收到片段就发布 `SessionEvent.Tool.Input.Delta`（含 `callID`/`delta`），`:9-12` 明确该事件只实时不落盘，完整 input 仍由 tool-call 事件承载。
- **argentgo 现状**：`internal/agent/events.go:74-75` 的 `tool_start` 只在工具开始执行时携带完整 `Args`（`:91`）；模型生成参数过程中没有任何增量事件。
- **问题**：长参数（整段补丁/脚本）生成期间界面无反馈，用户无法提前看到将要写入的路径或命令，也就无法在危险调用完成前按 Esc 取消。
- **建议**：默认在 agent 解析工具参数的流式路径上把每个参数片段经现有事件通道发一个 live-only 事件（带 callID），不写入 JSONL、不改 `tool_start`/`tool_end` 形状。
- **验收**：`go test ./internal/agent ./internal/server -run TestToolArgDeltaLiveOnly -count=1`。

### 42. 跨 harness 工具名别名 + 一次性更正提示 `[P2/S]`（学自 opencode）
- **竞品证据**：`packages/core/src/plugin/tool-input-repair.ts:22-24` 定义 `aliases = new Map([["bash","shell"]])`；`:36-44` 在 `execute.before` 钩子里仅当「没有任何工具叫这个名字」时才解析别名、改写 `event.tool`；`:50-69` 在 `execute.after` 追加 `There is no "bash" tool, so this ran as "shell". Call "shell" directly.`
- **argentgo 现状**：`internal/tools/builtin_register.go:36` 注册的工具名是 `bash`；无 aliases 映射、无执行后更正提示。
- **问题**：从 opencode/Claude Code 迁移来的模型常发 `shell`，在 argentgo 直接得到「unknown tool」错误，浪费一轮；`tool_search` 需要模型主动发现，不能兜底。
- **建议**：默认在 dispatch 前用配置化 `map[alias]target`（内置 `shell→bash`、`Bash→bash`），仅当别名未注册且目标存在时替换；执行结果末尾追加一行中文更正提示。
- **验收**：`go test ./internal/agent -run 'TestToolAliasFallback|TestToolAliasNoteOnlyWhenAliased' -count=1`。

### 43. 配置化 LSP server（initializationOptions / extensions / disabled） `[P2/M]`（学自 opencode）
- **竞品证据**：`packages/schema/src/config/lsp.ts:10-19` 的 `Config.LSP.Server` 支持 `command: string[]`、`extensions`、`disabled`、`env`、`initialization: Record<string,unknown>`，`Entry/Info` 允许整体布尔关闭或按 server 名覆盖；由配置驱动 registry。
- **argentgo 现状**：`internal/lsp/client.go:40-53` 的 `Server` 仅有 `Name/Command/Args/Env/LanguageIDs/RootURI`，无 InitializationOptions 字段；`internal/lsp/servers.go:13-29` 只有 4 个内置 `DefaultServers`；`internal/config/` 对 `lsp` 零命中——用户没有任何配置入口注册自定义 server 或传初始化参数。
- **问题**：大量语言服务器（pyright、部分 TS/自研 server）必须在 `initialize` 请求里带 `initializationOptions` 才能正确索引；用户也无法新增内置表之外的 server 或禁用某个 server，只能改源码。
- **建议**：默认在配置层解析 `lsp` 块（server 名 → command/args/env/extensions/initialization/disabled），经 `Registry.Register` 注入；`lsp.Start` 的 initialize params 带上 options；沿用七层配置优先级与 `--trust-project` 门控项目级配置。
- **验收**：`go test ./internal/lsp -run TestConfiguredServerInitializationOptions -count=1`。

### 44. checkpoint_diff 输出行级 hunks 与有界降级 `[P2/M]`（学自 dsh）
- **竞品证据**：`packages/deliverables/workspace-changes/src/compare.ts:38-65` 的 `compareText` 用超时有界的 structuredPatch 产出逐行 hunk，超时则降级为「整文件删除 + 整文件新增」的单个 coarse hunk，并单独标注 binary/oversized。
- **argentgo 现状**：`internal/tools/tools_checkpoint_diff.go:19-26` 的 `FileChange` 只有 `path/status/before_sha256/after_sha256/added/removed`；`:116-120 renderCheckpointDiff` 只渲染紧凑 JSON。模型拿到「某文件改了、±N 行」但看不到改了哪些行。
- **问题**：checkpoint_diff/undo 之后模型必须再对每个文件发一次 `read` 才能知道改动内容，多耗工具轮次与上下文；大 diff 上也没有有界降级策略。
- **建议**：默认用纯标准库实现逐行 LCS/统一 diff，给 `FileChange` 增加有界 hunk 行（带 `+N/-M` 头）；设行数/字节上限与超时（超时即只给 ±N 计数），binary/超大文件只给标志。
- **验收**：`go test ./internal/tools -run TestCheckpointDiffHunks -count=1`（含 CJK 行、超时降级）。

### 45. 进程级 pause 门：在模型/工具调用边界冻结全部 agent `[P2/M]`（学自 oh-my-pi）
- **竞品证据**：`oh-my-pi/packages/agent/src/pause.ts:25` 的 `AgentPauseGate` 是进程单例，`:42 pause()` 建 pending promise，`:74 waitUntilResumed(signal)` 在「每次模型调用前、每次工具启动前」两个安全点 park；在途 stream 与已启动工具跑完才停，排队 steering/follow-up 保留；单次运行的 AbortSignal 只释放本等待者，取消一条运行不必恢复整个进程。
- **argentgo 现状**：全仓零命中（`internal/agent/dispatch.go:155` 的 `progressGate` 是单个工具调用的进度发出器门，不是跨 agent 暂停）；无 `/pause` 命令或等价原语。
- **问题**：长任务里用户想临时冻结主 agent 与所有子 agent 时只能 Ctrl+C 全停，丢失在途工具结果与已缓存字节；无法「暂停观察、再继续」。
- **建议**：`internal/agent` 加 stdlib 门（`sync.Mutex` + channel），默认在 loop 的 Chat 前与 callTool 前各插一个可注入等待点；TUI `/pause`；ctx 取消只 unblock 当前运行，不改全局状态。
- **验收**：`go test ./internal/agent -run TestPauseGate -count=1`（暂停后新工具不启动、在途工具完成、单运行 abort 立即解阻塞而其他运行仍 parked）。

---

## 四、模型/协议与召回（46–49）

### 46. 有界判官目标循环（`/goal --max N`） `[P1/M]`（学自 OpenHands）
- **竞品证据**：`src/types/agent-server/core/events/conversation-state-event.ts:66-97` 的 `GoalVerdict`/`GoalStatus`——判官每次运行后给 `{score, complete, missing}`，状态机 `running|complete|capped|interrupted` 带 `iteration`/`max_iterations`；`src/hooks/chat/use-goal-interceptor.ts:11-14` 的 `MAX_FLAG` 解析 `--max N`，`:51-54` 组装 `StartGoalRequest`；`src/services/client-tool-result.ts:12-14` 规定任何入站消息立即取消活动 `/goal` 循环。
- **argentgo 现状**：`GoalVerdict`/`goal loop` 在 `*.go` 全部零命中；`internal/app/decision_judge.go:72` 的 `DecidePrompt` 是按提示文本推思考档位/模型/轮次上限的纯启发式，`:15-20` 明确不含完成判定；`internal/agent/budget.go:84 Breach` 只会停止，不会带 `missing` 反馈再驱动一轮。
- **问题**：用户要「直到确实做完」的长任务只能反复手动追问；模型提前结束或撞到预算即止，没有「判官说还差什么→自动续做」的闭环。
- **建议**：新增 `/goal [--max N] <objective>`（上限默认 10）；每轮结束用既有 secondary-model 调一次判官输出 `{complete,missing}`，未 complete 且未到上限时把 `missing` 作为 steering 注入下一轮；任何用户输入取消循环；新增 `goal_status` 事件。
- **验收**：`go test ./internal/agent -run TestGoalLoopBounded -count=1`（上限生效、用户输入取消）。

### 47. agent 自驱模型切换（带理由） `[P2/M]`（学自 OpenHands）
- **竞品证据**：`src/types/agent-server/core/base/action.ts:306-315` 的 `SwitchLLMAction` 带 `profile_name`+`reason`，描述要求模型说明「为什么这个 profile 更适合下一步」；`observation.ts:344-365` 的 `SwitchLLMObservation` 回 `active_model`/`reason`，`:367-390` 的 `ClassifyAndSwitchLLMObservation` 还回 `chosen_class`。
- **argentgo 现状**：`switch.*model` 在 `*.go` 的命中只在错误回退/展示/ACP；工具注册表无 switch/set_model 类工具；`internal/app/app_auto_decide.go:20 autoDecideEnabled` 是运行前按提示启发式选模型，模型自己无法在运行中请求换模型。
- **问题**：长任务中模型无法说「这步需要更强推理/更便宜模型」，用户只能预先配 fallback 或重启会话，做不了按步骤动态调配成本与质量。
- **建议**：默认注册 `switch_model{profile_name,reason}`，按已配置 profile 白名单匹配（不在名单拒绝并给纠正指引），成功后回灌 `active_model`+reason 并记可审计事件。
- **验收**：`go test ./internal/app -run TestSwitchModelToolAllowlist -count=1`。

### 48. 被拒 refresh token 的短期负缓存 `[P2/S]`（学自 crush）
- **竞品证据**：`crush/internal/config/store.go:1084` 的 `ConfigStore.exchange` 调 provider 前先查内存负缓存；`:1101-1106` 当错误判为 refresh token revoked 时按 token 记住错误 `until = now + 5min`（`rejectedRefreshTokenTTL`），期间同 token 直接返回缓存错误，过期即删。
- **argentgo 现状**：`internal/auth/pkce.go:198` 的 `Refresh` 是无状态单次函数；全仓 `internal/auth` 无负缓存符号，token 过期后每次失败都重新打 provider 端点。
- **问题**：凭据被撤销或短期故障时，每个请求都触发失败的 refresh 交换，产生请求风暴与重复告警，日志/配额被噪声淹没。
- **建议**：默认在 `auth` 包加有界 map（token→err/expiry，TTL 5 分钟、条目上限 100），包装 `Refresh`：命中未过期拒绝直接返回缓存错误；过期删除；仅对「refresh token 被撤销」类错误写入。
- **验收**：`go test ./internal/auth -run TestRefreshRejectedNegativeCache -count=1`（5 分钟内同 token 只交换一次、过期后重试）。

### 49. 按 provider/model/模式动态开关工具 `[P2/M]`（学自 cline）
- **竞品证据**：`cline/sdk/packages/core/src/extensions/tools/model-tool-routing.ts:105` 的 `resolveToolRoutingConfig` 按 `providerIdIncludes`/`modelIdIncludes`/`mode` 匹配规则，合并 `enableTools`/`disableTools`；`:60` 默认规则对 OpenAI-native/codex/gpt 在 act 模式启用 `apply_patch` 并禁用 `editor`。
- **argentgo 现状**：`internal/app/mcp.go:69-78 wireOptInTools` 与 `internal/config/optin_config.go:41 apply_patch` 都是全局布尔 opt-in；grep `modelIdIncludes`/`ToolRouting` 零命中。工具目录一旦装配就与当前 provider/model 无关。
- **问题**：原生支持 `apply_patch` 的模型仍同时看到 `editor`，工具选择歧义、补丁格式不统一；不支持补丁的模型若被强制切到 apply_patch 则会失败。
- **建议**：默认纯标准库规则表：按当前 `providerID`/`modelID`（大小写不敏感子串）与模式对指定工具做 enable/disable，在工具目录装配后、系统提示生成前应用，只收紧不新增工具；内置规则「openai-native/gpt/codex + act 模式 → 开 apply_patch、关 editor」。
- **验收**：`go test ./internal/app -run 'TestResolveModelToolRouting|TestToolRoutingDefaultUnchanged' -count=1`。

---

## 五、交互界面与可用性（50–54）

### 50. 任务列表陈旧/未完成的模型可见提醒 `[P1/S]`（学自 killi）
- **竞品证据**：`crates/killi/src/agent/dispatch.rs:428-430` 在成功 `todo_write` 后清零、其余工具调用累加 `calls_since_todo`；`turn.rs:312-317` 在 `>=STALE_TASKS_AFTER(10)` 且有未完项时追加一次 `stale_tasks_note`（`:409`，点名当前项、截断 120 字符），`:197` 轮末用 `open_tasks_note`（`:422`）提醒未完项；`docs/decisions.md:320` 记录 33 个 turn 中 23 个出现 ≥10 次工具调用而列表不动。
- **argentgo 现状**：`internal/tools/todo_validate.go:50 ValidateTodoState` 是写入前结构校验（`ARGENT_TODO_STRICT`），没有「工具调用计数 → 提醒模型更新 todo」的路径；`internal/agent/loop.go` 无 `callsSinceTodo` 等价状态。
- **问题**：任务实际做完但界面仍显示 `0/8`、首项 in_progress，模型与用户都失去进度信号；killi 实测仅靠 prompt 说明无效。
- **建议**：默认 `runState` 加 `callsSinceTodo`，工具成功派发后累加、`todo_write` 成功时清零；请求前 `>=10` 且有非 completed 项时用 `injectSteeringAs(extra...)` 追加点名提醒（每 10 次一次）；轮末有未完项再追加一条 open tasks 提醒。
- **验收**：`go test ./internal/agent -run 'TestStaleTodoNudge|TestOpenTasksNudge'`（含关闭时消息逐字节不变）。

### 51. 未完成回复的续做提醒（冒号/意图句） `[P1/S]`（学自 killi）
- **竞品证据**：`crates/killi/src/agent/ending.rs:27` 的 `ends_with_colon` 用 `last_line_ends_with(text, [':','：'])` 判定，`:34 unfinished` 在无工具调用时返回一次性 `UNFINISHED` reminder，`:13` 还覆盖「我接下来检查…」式意图句；`docs/decisions.md:329` 记录 223 个无工具轮里 2 个以冒号收尾，提醒后模型用工具接着做完。
- **argentgo 现状**：`internal/agent/` 无 `ends_with_colon`/`unfinished` 等价逻辑；轮末只有统计、耗时直方图与调用轨迹日志，没有针对「承诺了却没做」的纠正注入。
- **问题**：模型输出「正在排查：」后结束轮次，用户以为卡住还得手动 `continue`；一次短提醒即可省一轮往返。
- **建议**：默认轮末若无工具调用且最后非空行以 `:`/`：` 结尾（或命中「我先查/接下来…」短句），用 `injectSteeringAs` 注入一次性 reminder（问句结尾不触发以避让用户提问）；每轮至多一次、仅顶层。
- **验收**：`go test ./internal/agent -run TestUnfinishedNudge`（半角/全角冒号、问句不触发、代码块不触发）。

### 52. 循环检测在用户插话后重置并升级为带证据提示 `[P1/S]`（学自 dsh）
- **竞品证据**：`packages/guard/repeat-tool-reminder/src/index.ts:196-212` 的 `observe` 以 `canonicalize`（对象键深度排序）为签名维护每 agent 连续计数，命中 `thresholds`（默认 `[3,5,8]`）时分别注入温柔提示与带工具名、次数、限长参数预览的 `detailedReminder`；关键在 `:228-231` 的 `agent/pre-step` 监听——只要本步含用户消息就 `chains.delete(agent)` 重置计数。
- **argentgo 现状**：`internal/agent/loop.go:766-779 toolStepsFromEvents` 从整个 run 的 `EventToolEnd` 投影步序列，用户 steering 消息不参与也不切断；`:789-807 checkLoopDetection` 委托 `loop_detection.go:60-99 DetectLoop`（窗口 10）后只注入固定一句话 `loopDetectionGuidance`。源码零命中「用户插话重置」与分级/带工具名提示。
- **问题**：用户中途纠正（steering）后，纠正前的重复调用仍在 10 步窗口内，模型本已改方法却收到「你在重复调用」的错误提示；单一模糊提示也不告诉模型是哪个工具在打转。
- **建议**：默认在 `injectSteering` 的用户注入点清空 `runState` 的循环步窗口（或记录序号水位，只投影水位之后的 `tool_end`）；提示按次数升级（3/5/8），附带工具名与 head 截断的规范化参数（脱敏、有界）。
- **验收**：`go test ./internal/agent -run 'TestLoopDetectionResetsOnSteer|TestLoopDetectionEscalatingNudge' -count=1`。

### 53. TUI 粘贴文件路径识别为附件 `[P2/S]`（学自 crush）
- **竞品证据**：`crush/internal/fsext/paste.go:8` 的 `ParsePastedFiles` 先剥离 Rio 在 Windows 插入的 `\x00`，再按三路解析：逐行 `os.Stat` 全命中即按行返回、WT_SESSION 下解析引号包裹路径、Unix 下解析反斜杠转义空格。
- **argentgo 现状**：TUI 只有 bracketed paste 的原始文本解码（`internal/tui/keys.go:100 KindPaste`），把粘贴内容整块塞入输入框；`ARGENT_IMAGE_INPUT` 只从用户原文扫描 `@路径`，不识别直接粘贴的引号/转义文件路径。
- **问题**：从资源管理器或终端粘贴多个文件路径时，Windows Terminal 的引号、Unix 的 `\ ` 转义与 Rio 的 NUL 残留进入提示词成为字面量，模型拿到坏路径、附件识别失败。
- **建议**：默认纯标准库移植 `ParsePastedFiles` 三路判定，在提交前把识别出的合法存在文件转换为 `@path` 引用/附件；仅当整段文本按规则全部解析为存在文件时才改写，否则原样提交。
- **验收**：`go test ./internal/tui -run 'TestParsePastedFilesWT|TestParsePastedFilesUnix|TestParsePastedFilesRioNull'`。

### 54. TUI 输入行 Emacs kill-ring：连续删除合并 + yank-pop 循环 `[P2/S]`（学自 pi-mono）
- **竞品证据**：`pi-mono/packages/tui/src/kill-ring.ts:8` 的 `KillRing` 是有界字符串环：`:19 push` 在 `accumulate` 时按 prepend/append 把连续 kill 合成一条，否则新建条目；`:36 rotate` 把最新条移到队首实现 yank-pop 循环；`:31 peek` 取最近条目而不改环。
- **argentgo 现状**：TUI 输入行只有 `HistoryPrev/HistoryNext`（`internal/tui/model_input.go:209/230`）与 vim 键位（`ARGENT_VIM_MODE`），全仓无 kill ring。
- **问题**：交互用户一次性删掉长路径/多段文本后，只能找回最后一次删除的内容，连续删除的片段无法用 yank-pop 逐个召回，长命令编辑体验明显低于主流终端 agent。
- **建议**：`internal/tui` 加 stdlib `killRing`（有界、按 rune 安全），接删除键与 `Ctrl+Y` yank / `Alt+Y` yank-pop；与 `ARGENT_VIM_MODE` 正交。
- **验收**：`go test ./internal/tui -run 'TestKillRing|TestKillRingKey' -count=1`（连续两次向前删合并为一条、yank-pop 循环、CJK 按 rune 不切坏）。

---

## 六、生态与扩展（55–56）

### 55. 子代理任务按 id 续跑 `[P1/S]`（学自 OpenHands）
- **竞品证据**：`src/types/agent-server/core/base/action.ts:287-304` 的 `TaskAction` 带 `resume?: string`（"Task id to resume from, when continuing an existing subagent task"）与 `subagent_type`；`observation.ts:308-329` 的 `TaskObservation` 返回 `task_id`/`subagent`/`status`，使父级能持久引用子任务。
- **argentgo 现状**：`internal/agent/subagent.go:232-248` 的 `taskArgs` 字段仅 description/prompt/task/persona/role/verify/verify_max_retries/background，无 `resume`；`:257 runTask` 只有一次性、验证重试、后台三条路径。
- **问题**：已完成的子代理若有后续问题，父级只能从头再派一次，重复昂贵验证并丢失子级上下文。
- **建议**：默认给 task 工具加 `resume` 参数，从会话子代理记录按 id 找回其转录与尾部状态，以 `OpenSessionAt` 方式续写并回灌新结果；找不到 id 返回可读错误。
- **验收**：`go test ./internal/agent -run TestTaskResumeById -count=1`。

### 56. 轻量隔离子任务（worktree + 降级说明） `[P2/M]`（学自 OpenHands）
- **竞品证据**：`src/services/child-conversation-launch.ts:240-242` 的 `parentSupportsWorktree` 先判父工作区能否切 worktree（无提交 scratch 目录不行），`:279-317 launchLocalChild` 以 `workspaceMode:"new_worktree"` 启动子会话，失败回退 `"shared"` 并附 `isolation_note` 说明后果，`:247-263 parentProfileLaunch` 让子级继承父级 profile/LLM，`:519-528` 用领取账本防重复启动。
- **argentgo 现状**：worktree 基础设施已存在但只用于 eval（`internal/eval/history.go`、`internal/app/worktree_wire.go`）；`internal/agent/subagent.go` 的 `task` 在父进程同一工作区跑子循环，`taskArgs` 无 `isolation`/`branch`。
- **问题**：多个子代理并行改同一工作区会互相踩文件；想要「各改各分支再合并」只能用户手动建 worktree。
- **建议**：默认给 task 加 `isolation: worktree|shared`（默认 worktree，因隔离是安全默认）；复用既有原语建临时分支目录，结束保留目录并回路径；无 git/无提交时回退 shared 并附降级说明。
- **验收**：`go test ./internal/agent -run TestTaskWorktreeIsolationFallback -count=1`。

---

## 附：优先处理建议

> 顺序综合了 8 个调研代理的结论。P0 先做（安全/数据完整性），再做 P1（体验缺口/原语接线），最后 P2。每条落地时按「默认化原则」：功能默认开、参数给最优值、不留用户选项。

1. **第一优先（P0，安全与数据完整性，5 条）**：#17 非幂等工具领取账本、#18 密钥请求工具、#19 裁剪缺失不构成授权、#20 中断告示部分执行、#31 前台长 sleep 拒绝。这五条直接防止重复副作用、密钥泄露与 fail-open 越权。
2. **第二优先（上下文与压缩，#1–#16）**：#1/#2/#3 是压缩正确性与成本的三连击（缩减门禁 / KV 缓存 / 自身超限恢复）；#14 指令变更重注入与 #26 嵌套规则提醒是用户最直观的「改了规则立刻生效」。
3. **第三优先（工具执行，#31–#45）**：#31 已列 P0；#33/#34/#35/#36/#37 是输出/写盘的确定性与效率（URI 缓存、EOL、BOM、截断计数、JSON 转义预算），适合一批做完。
4. **安全边界其余（#21–#30）**：`AGENTS.md` 保护、密钥文件收窄、真人交互审批、fork 边界、作用域回灌是防御纵深，与 P0 五条同属安全批次。
5. **交互与生态（#50–#56）**：小改动、体验直接；可与主批次并行。
6. **验证方式**：每条「验收」栏给了可执行命令。因本轮功能默认开启，金标准改为「默认路径产出新行为且测试锁定」；涉及回退路径的用 `=0` 验证兼容性。`gofmt -l .`、`go vet ./...`、`go test -p 4 ./... -count=1` 每轮必跑，发版门禁再跑 `-race` 与 `GOOS=linux go build ./...`。

## 附：参数归位（历史 74 个 opt-in 的默认化）

> 按用户 2026-11-23 指示「参数调整到最优，不要让用户自己头疼选择」，现有 opt-in 属历史债务，**最后一批**处理。原则：验证稳定且改善体验的逐条改为默认开、参数取最优值，移除用户文档中的「需手动开启」表述，仅保留 `=0` 紧急回退；确有争议或有合规/兼容性代价的保留开关但给出最优默认值。每批默认化都要同步更新 golden/测试基线与 `docs/OPT_INS.md`、`internal/cli/optin_metadata.go`。

- **第一批（纯增益、零副作用，优先默认化）**：`tool_timing_histogram`、`tool_call_trace`、`config_reload_diff`、`cache_visible`、`tui_badge_contrast`、`edit_diagnostics`、`read_before_write`、`grep_per_file`、`output_collapse`、`mcp_output_limit`——只增信息/只优化展示，不改变决策与工具语义。
- **第二批（正确性修复，应默认开）**：`bash_use_proc`（已默认）、`lsp_kill_tree`、`lsp_sanitize_env`、`mcp_readonly` 相关、`checkpoint_guard`、`loop_detection`、`retry_omit`、`parse_regression_guard`、`audit_sanitize`、`hook_verdict`。
- **第三批（改善体验，参数取最优值）**：`background_compact`（lead 0.8）、`context_calibration`（factor 钳 1..4）、`foveated_archive`、`continuation_brief`、`output_spill`、`approval_persist`、`staged_approval`、`approval_ttl`、`plan_mode`、`jobs`、`persistent_shell`、`exec_session`。
- **第四批（有兼容性/合规代价，保留开关但最优默认）**：`image_input`、`mcp_gateway`、`mcp_injection_scan`、`trust_project`、`locale`、`eval_live`、`telemetry`——这些涉及外部服务、凭据或协议行为，默认化前需单独评审并向用户说明影响。
- **收尾**：默认化完成后，把 `AGENTS.md` 的「新能力 opt-in 默认关闭、默认路径逐字节不变」改写为「改善体验的新能力默认开启、参数取最优值、保留 `=0` 紧急回退」，并更新 `STATUS.md` 的 Hard constraints 与 `docs/OPT_INS.md` 的开关清单。

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
