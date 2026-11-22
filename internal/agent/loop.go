package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/hooks"
	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// cancelledCallReason 是为从未运行的调用写入的工具结果。
const cancelledCallReason = "tool did not run: the run was cancelled"

// continueAfterOutputLimit 是模型在请求工具之前耗尽输出 token 时所追加的合成用户轮次。
// 对应 OpenCode 的 CONTINUE_AFTER_OUTPUT_LIMIT：模型通常知道还该说什么，
// 只是需要一个新的输出预算，以便用更小的片段说出来。
const continueAfterOutputLimit = "Continue the work, but break the remaining steps into smaller pieces."

// emptyReplyCorrection 是当模型返回没有可见答案的回复（空回复或纯推理回复）时追加的合成用户轮次。
// 它修复的是与 continueAfterOutputLimit 相反的失败：那里模型说太多而被截断，
// 这里它什么也没说，但运行却会被报告为成功完成。
const emptyReplyCorrection = "Your last reply contained no answer. Please respond with a visible answer or the next step."

// maxConsecutiveEmptyReplies 限制循环对空回复或纯推理回复进行提示、要求其产出答案的次数。
// 没有上限时，一直以空内容回复的模型会空转到轮次预算耗尽。
const maxConsecutiveEmptyReplies = 2

// maxConsecutiveLengthFinishes 限制循环在输出长度截断后要求模型继续的次数。
// 没有上限时，总是触及同一限制的模型会让运行一直持续到轮次预算耗尽。
const maxConsecutiveLengthFinishes = 2

// defaultTrimTargetRatio 是应用于裁剪预算的迟滞比率：Trim 以 MaxTokens 的这个比例为目标，
// 使一次成功的裁剪留出余量，下一轮次不会立即再次触发裁剪（那会使提示缓存失效）。
// 对应 gptme 的 0.7 目标。
const defaultTrimTargetRatio = 0.7

// stopRepeatedMistakes 在连续工具失败达到错误追踪器的限制且没有可用恢复指引时结束运行。
// 它是内部停止原因：公开的 Stop* 常量不变。
const stopRepeatedMistakes = "repeated_mistakes"

// defaultMistakeGuidance 在一连串工具失败后，通过要求模型换一种方法来维持运行。
// 追踪器只负责判断；该指令会在下一次进入循环时作为用户轮次注入。
func defaultMistakeGuidance(MistakeKind, int) string {
	return "连续出错，请换一种方法或换模型"
}

// loopDetectionGuidance 是工具调用循环检测触发时注入的模型可读提示。
// 与 stuck/mistake 指引一致，它是一条新的用户轮次，要求模型换一种方法，
// 而不是硬性停止——保留模型自行恢复的能力。仅在 SetLoopDetection 开启时使用。
const loopDetectionGuidance = "检测到重复调用同一工具且返回相同结果，并未取得进展；请换一种工具、参数或方法继续。"

// runState 是一次运行的可变状态。子 agent 拥有自己的状态，
// 因此除了配置的接口之外，不会与父级共享任何东西。
type runState struct {
	opts     Options
	model    string
	tools    ToolRunner
	specs    []llm.ToolSpec
	messages []llm.Message

	emitFn func(Event)
	rec    Recorder
	steer  *steering
	guard  *dedupe

	// ttsr 是流式规则观察器。nil（默认）时 chatWithTTSR 直接走 s.chat，逐字节不变；
	// 非 nil 时按规则在流中判定 Retry/Inject/Warn，见 chatWithTTSR。
	// ttsrRetries 记录本轮已用掉的"命中后重发"次数（每轮最多一次）。
	ttsr        *TTSRWatcher
	ttsrRetries int

	// beforeRequest 和 beforeTool 是本次运行注入的持久性接缝，复制自 Options。
	// nil 钩子是空操作，因此手工构建的 runState（例如测试中）保持原有行为。
	beforeRequest func() error
	beforeTool    func() error
	// mistakes 跨轮次统计连续的工具失败。接收者可为 nil 的方法使手工构建的 runState
	// （例如测试中）在没有它的情况下也安全。
	mistakes *MistakeTracker
	// invalidBreaker 统计一个轮次内的参数校验拒绝。它由 dispatch 的 breaker() 延迟创建，
	// 并在每次进入 runTools 时重置，因此手工构建的 runState 无需显式构造。
	invalidBreaker *InvalidArgBreaker
	// stuck 监视事件模式中的重复循环。接收者可为 nil 的 Observe
	// 使手工构建的 runState 在没有它的情况下也安全。
	stuck *StuckDetector
	// guidance 记录失败指引的去重状态；零值即可使用。只有在 guidanceDedupe
	// opt-in 开启时才被查询，默认运行完全不触碰它。
	guidance GuidanceState
	// loopGuidance 对循环检测触发的提示按签名去重，使同一个循环在一次运行里
	// 只提示一次（按情节）。只有在 SetLoopDetection opt-in 开启时才被查询。
	loopGuidance GuidanceState

	// recovery 是 ARGENT_RECOVERY_METRICS opt-in 开启时创建的恢复成效账本。
	// 默认 nil：所有记录/读取路径都是空操作，事件、消息与会话记录逐字节不变。
	recovery *RecoveryMetrics
	// recoveryPendingKind 记录最近一次注入指引时的错误种类，使随后 loop 侧可见的
	// "成功"信号（无指引动作）能归到同一种类。仅在 recovery 非 nil 时有意义。
	recoveryPendingKind string

	usage        llm.Usage
	costUSD      float64
	costKnown    bool
	costWatch    *CostWatcher
	promptTokens int
	turns        int
	depth        int
	// tokenForecast 是 ARGENT_TOKEN_FORECAST opt-in 的置位。默认 false 时用量历史不追加、
	// 请求前不预测、不发事件，消息与事件流逐字节不变。
	tokenForecast bool
	// usageHistory 记录最近 usageHistoryLimit 轮的每轮（prompt+completion）用量，供
	// ForecastNextTokens 外推；仅 opt-in 开启时预分配。forecastCompacted 使提前压缩
	// 每次运行最多触发一次，避免同一段超预算趋势每轮都重复告警。
	usageHistory      []int
	forecastCompacted bool
	// 预算提醒按上下文窗口划分：delivered 记录哪些占用层级已播报过，
	// reminderWindow 是计算这些层级时所依据的窗口大小。压缩会重置两者，
	// 使新窗口能再次播报其层级。
	reminderDelivered map[int]bool
	reminderWindow    int
	// bgOwner 是本次运行在 Subagent.Background 中的投递身份。它与 depth 不同：
	// 同级子 agent 共享相同的深度，但绝不能抽取彼此的后台结果。
	bgOwner  uint64
	maxTurns int
	lastText string
	trim     TrimOptions
	events   []Event
	// eventsMu 串行化事件记录：流式工具的进度回调可能在循环发出轮次边界时
	// 运行于自己的 goroutine 上。
	eventsMu sync.Mutex
	// 出于同样的原因，recordMu 串行化对 recorder 的写入。
	recordMu sync.Mutex

	// progress 是轮次内进度捕获器（写侧）。nil 表示未注入 sink（默认），
	// 整段进度写入路径是空操作。
	progress *progressCapture

	// 向外部世界的投递（emitFn 和旧版 Out 写入器）发生在 eventsMu 之外，
	// 这样缓慢的消费者不会阻塞事件记录或轮次边界。emitSeq 是下一个追加顺序票号，由 eventsMu 保护；
	// emitDelivered 是下一个允许投递的票号，由 emitSendMu 保护。条件门控使 emitFn 观察到的顺序
	// 与追加顺序完全一致，即使流式工具的进度回调是从 worker 发出的。
	emitSeq       uint64
	emitSendMu    sync.Mutex
	emitDelivered uint64
	emitOnce      sync.Once
	emitCond      *sync.Cond

	// toolMeta 是工具运行器产生的进程内元数据，以 ToolCallID 为键。并行批次会从 worker goroutine
	// 写入不同的键，因此访问受锁保护。它从不持久化：恢复后常规的基于 token 的裁剪仍然有效。
	metaMu   sync.Mutex
	toolMeta map[string]ToolMeta

	// 可选的 StartedAtMS/FinishedAtMS 事件字段所用的墙上时钟计时。
	// toolTimes 以 ToolCallID 为键，使并行调用独立计时；timingMu 保护它以及唯一的在途推理窗口。
	// nil 映射会延迟创建，因此手工构建的 runState 仍然安全（计时为空操作）。
	// 这些值从不在此持久化：它们只是为选择消费它们的前端/会话层丰富事件。
	timingMu       sync.Mutex
	toolTimes      map[string]time.Time
	reasoningStart time.Time

	// turnTiming 是 ARGENT_TURN_TIMING opt-in 开启时创建的逐阶段计时器。
	// nil（默认，未开启）时所有 Begin/End、快照与日志全是空操作，
	// 事件序列与会话记录逐字节不变。它与上面的 eventTiming 正交：
	// 后者服务于前端 StartedAtMS/FinishedAtMS 字段，本字段服务于进程内结构化诊断日志。
	turnTiming *TurnTiming

	// toolTimingHistogram 是 ARGENT_TOOL_TIMING_HISTOGRAM opt-in 开启时创建的工具耗时直方图。
	// nil（默认，未开启）时 dispatch 侧的 Record 与轮末摘要日志全是空操作，
	// 事件序列与会话记录逐字节不变。它与 turnTiming 正交：可单独开启其中任意一个。
	toolTimingHistogram *ToolTimingHistogram

	// toolCallTrace 是 ARGENT_TOOL_CALL_TRACE opt-in 开启时创建的工具调用链追踪器。
	// nil（默认，未开启）时 dispatch 侧的 Begin/End 与轮末摘要日志全是空操作，
	// 事件序列与会话记录逐字节不变。它与 turnTiming/toolTimingHistogram 正交：
	// 直方图给分布/TopSlow，trace 给调用顺序、成败与错误文本。
	toolCallTrace *ToolCallTrace
	// toolCallSpans 以调用 id 为键记录 ToolCallTrace.Begin 返回的 spanID，
	// 使 finishCall 能配对结算同一跨度。与 toolTimes 共用 timingMu 保护；
	// 并行批次写入不同 id，结算后立即删除。
	toolCallSpans map[string]int

	// contextHealthLog 是 ARGENT_CONTEXT_HEALTH_LOG opt-in 开启时置位的诊断开关。
	// 默认 false：轮末投影与日志全是空操作，消息、事件与会话记录逐字节不变。
	// 它是纯观测：只读取当前消息样本并输出一条 slog，不改任何模型输入、裁剪或决策；
	// 刻意不走 SetContextHealthLogger 这个全局钩子，避免跨运行并发设置的数据竞争。
	contextHealthLog bool

	// pressureLog 是 ARGENT_PRESSURE_LEVEL opt-in 开启时置位的诊断开关。
	// 默认 false：轮末的压力分级与日志全是空操作，消息、事件与会话记录逐字节不变。
	// 它是纯观测：只读取当前用量与预算、调用纯函数 ClassifyPressure 落一条 slog，
	// 不改任何模型输入、裁剪或决策；不产生 EventNotice，不改提醒文案与模型可见字节。
	pressureLog bool

	// compactionPolicy 是 ARGENT_COMPACTION_POLICY opt-in 开启时置位的诊断开关。
	// 默认 false：压缩触发点的策略观测整段是空操作，消息、事件与会话记录逐字节不变。
	// 它纯观测：只读取当前信号、调用纯函数 SelectCompactionAction 并输出一条日志，
	// 不改压缩阈值、裁剪、压缩器调用或任何模型输入；既有阈值路径保持原样。
	compactionPolicy bool

	// compactor 和 compactThreshold 驱动轮次中压缩的边界。
	compactor        Compactor
	compactThreshold int

	// compactRetries 统计本次运行已经用掉的"压缩后重放"次数。上限 MaxCompactRetries 为 1，
	// 因此该计数同时是 once-per-step 的防空转保护：重放一旦发生，后续溢出都会直接停止。
	compactRetries int

	// consecutiveLengthFinishes 统计因输出长度而结束、且中间没有其他非长度回复的回复数。
	// 它驱动无工具答案的继续预算，并在每次未被截断的回复时重置。
	consecutiveLengthFinishes int

	// consecutiveEmptyReplies 统计没有可见答案、且被提示重新产出答案的无工具回复数。
	// 可见答案或工具批次会清除该连续序列；达到 maxConsecutiveEmptyReplies 时停止运行，
	// 使从不作答的模型无法空转到轮次预算结束。
	consecutiveEmptyReplies int

	// verifyState 限制本次运行的编辑后验证反馈。只有在设置了 Options.Verify 时才会查询它，
	// 因此未配置的运行绝不会触碰它。
	verifyState VerifyLoopState
	// batchVerificationEligible 记录最近处理的批次是否经过正常派发且包含变更型调用。
	// 被整体拒绝的批次（参数被截断、BeforeTool 钩子失败或调用 id 无效）改由 fillUnrun 回应；
	// 没有发生编辑，因此编辑后验证既不应消耗一次反思，也不应停止运行。
	// 已派发批次中个别被重复防护阻断的调用仍有资格：那是模型自己失败的编辑尝试，
	// 而不是循环层面的拒绝。
	batchVerificationEligible bool
}

// execute 是 Run、RunText 和 Prompt 共享的内部入口。
func (r *Runner) execute(ctx context.Context, prompt string, emit func(Event)) (Result, error) {
	opts, err := r.resolved()
	if err != nil {
		return Result{}, err
	}
	state := newRunState(opts, prompt, emit)
	// 按源前缀哈希缓存注入的压缩器，使同一 Runner 上不同提示中的相同前缀不会被重复摘要。
	// 未配置压缩器时，该包装器是透明的。
	state.compactor = newCachingCompactor(state.compactor, r.compactionCache())
	res, err := state.loop(ctx)
	// opt-in 开启时把本轮各阶段计时固化为快照并落结构化日志。关闭时 state.turnTiming
	// 为 nil，整段是空操作：事件、会话记录与返回值逐字节不变。
	state.finishTurnTiming(res.Stopped)
	// 工具耗时直方图的轮末摘要日志，与逐阶段计时日志并列、共用同一 logger。
	// 关闭时 state.toolTimingHistogram 为 nil，整段是空操作。
	state.finishToolTimingHistogram()
	// 工具调用链的轮末摘要日志，与上面两条日志并列、共用同一 logger。
	// 关闭时 state.toolCallTrace 为 nil，整段是空操作。
	state.finishTrace()
	// 上下文健康度的轮末观测日志，与上面三条日志并列、共用同一 logger。
	// 关闭时 state.contextHealthLog 为 false，整段不采样、不输出。
	// 纯观测：只读消息样本落一条 slog，不改模型输入、裁剪或运行决策。
	state.finishContextHealth()
	// 上下文压力等级的轮末观测日志，与上面四条日志并列、共用同一 logger。
	// 关闭时 state.pressureLog 为 false，整段不分级、不输出。
	// 纯观测：只读用量与预算落一条 slog，不改模型输入、事件或运行决策。
	state.finishPressure()
	if state.opts.OnCompactionEvidence != nil {
		state.opts.OnCompactionEvidence(CollectCompactionEvidence(state.eventsSnapshot(), res.Messages))
	}
	// 通过注入的 Recorder 持久化一条运行总计的 stats 记录。app 把 Record 接到 Session.Append，
	// 因此它会成为一条 kind:"stats" 记录，供 LatestStats/AggregateStats 在恢复后读回。
	// 没有 recorder 时为空操作。被取消的运行会跳过此步，使其记录仍以合成的终结助手消息结束
	// （recorder 的最后一行保持为 "assistant"）；这类运行仍会持久化 account() 写出的逐次用量。
	if res.Stopped != StopCancelled {
		state.recordRunStats()
	}
	return res, err
}

// turnTimingEnabled 报告本进程是否开启逐阶段计时日志。
// 与 app 层的一次性 env 开关（ARGENT_JOBS、ARGENT_RETAIN_GUIDANCE）一致：
// 仅当 ARGENT_TURN_TIMING 去空白后等于 "1" 时开启，默认（未设置/空/其它值）为关。
// 每次运行读取一次（见 newRunState），不引入需要装配层设置的进程级全局状态。
func turnTimingEnabled() bool {
	return strings.TrimSpace(os.Getenv("ARGENT_TURN_TIMING")) == "1"
}

// recoveryMetricsEnabled 报告本进程是否开启恢复成效账本。
// 仅当 ARGENT_RECOVERY_METRICS 去空白后等于 "1" 时开启，默认（未设置/空/其它值）为关。
// 与 turnTiming 一样在 newRunState 中读取一次，不引入需要装配层设置的进程级全局状态。
func recoveryMetricsEnabled() bool {
	return strings.TrimSpace(os.Getenv("ARGENT_RECOVERY_METRICS")) == "1"
}

// toolTimingHistogramEnabled 报告本进程是否开启工具耗时直方图日志。
// 仅当 ARGENT_TOOL_TIMING_HISTOGRAM 去空白后等于 "1" 时开启，默认（未设置/空/其它值）为关。
// 与 turnTiming 一样在 newRunState 中读取一次，不引入需要装配层设置的进程级全局状态。
func toolTimingHistogramEnabled() bool {
	return strings.TrimSpace(os.Getenv("ARGENT_TOOL_TIMING_HISTOGRAM")) == "1"
}

// toolCallTraceEnabled 报告本进程是否开启工具调用链日志。
// 仅当 ARGENT_TOOL_CALL_TRACE 去空白后等于 "1" 时开启，默认（未设置/空/其它值）为关。
// 与 turnTiming 一样在 newRunState 中读取一次，不引入需要装配层设置的进程级全局状态。
func toolCallTraceEnabled() bool {
	return strings.TrimSpace(os.Getenv("ARGENT_TOOL_CALL_TRACE")) == "1"
}

// contextHealthLogEnabled 报告本进程是否开启上下文健康度诊断日志。
// 仅当 ARGENT_CONTEXT_HEALTH_LOG 去空白后等于 "1" 时开启，默认（未设置/空/其它值）为关。
// 与 turnTiming 一样在 newRunState 中读取一次，不引入需要装配层设置的进程级全局状态。
func contextHealthLogEnabled() bool {
	return strings.TrimSpace(os.Getenv("ARGENT_CONTEXT_HEALTH_LOG")) == "1"
}

// pressureLevelLogEnabled 报告本进程是否开启上下文压力等级诊断日志。
// 仅当 ARGENT_PRESSURE_LEVEL 去空白后等于 "1" 时开启，默认（未设置/空/其它值）为关。
// 与 turnTiming 一样在 newRunState 中读取一次，不引入需要装配层设置的进程级全局状态。
func pressureLevelLogEnabled() bool {
	return strings.TrimSpace(os.Getenv("ARGENT_PRESSURE_LEVEL")) == "1"
}

// compactionPolicyEnabled 报告本进程是否开启压缩策略 opt-in。
// 仅当 ARGENT_COMPACTION_POLICY 去空白后等于 "1" 时开启，默认（未设置/空/其它值）为关。
// 与 turnTiming 一样在 newRunState 中读取一次；默认关闭时压缩触发点完全走原有阈值路径，
// 不构造输入、不调用 SelectCompactionAction、不输出日志，既有行为逐字节不变。
func compactionPolicyEnabled() bool {
	return strings.TrimSpace(os.Getenv("ARGENT_COMPACTION_POLICY")) == "1"
}

// usageHistoryLimit 是预测所用的有界用量历史长度：长时间运行只保留最近若干轮，
// 使 ForecastNextTokens 的小窗口外推始终贴近最新趋势，且内存占用有上界。
const usageHistoryLimit = 20

// tokenForecastEnabled 报告本进程是否开启请求前的 token 用量预测。
// 仅当 ARGENT_TOKEN_FORECAST 去空白后等于 "1" 时开启，默认（未设置/空/其它值）为关。
// 与其余 opt-in 一样在 newRunState 中读取一次，不引入需要装配层设置的进程级全局状态。
func tokenForecastEnabled() bool {
	return strings.TrimSpace(os.Getenv("ARGENT_TOKEN_FORECAST")) == "1"
}

// compactionPolicyLogger 返回输出压缩策略决策日志的 logger。
// 它是函数而非固定实例：每次落日志时解析当前 slog 默认 logger，测试可整体替换以捕获日志，
// 与 turnTimingLogger 的模式一致，不引入需要装配层设置的进程级全局状态。
var compactionPolicyLogger = func() *slog.Logger { return slog.Default() }

// turnTimingLogger 返回输出逐阶段计时结构化日志的 logger。
// 它是函数而非固定的 *slog.Logger：每次落日志时解析当前 slog 默认 logger，
// 避免包初始化时把默认 logger 固化；测试可整体替换以捕获日志而不碰全局默认。
var turnTimingLogger = func() *slog.Logger { return slog.Default() }

// turnTimingPhases 是落日志时固定输出的阶段顺序，保证字段稳定、便于断言。
var turnTimingPhases = [...]TurnPhase{
	phaseInference, phaseTools, phaseCompaction, phaseUserWait, phaseOverhead,
}

// emitTurnTimingLog 把一个轮次的计时快照写成一条结构化日志。
// 用微秒整数表达各阶段耗时，避免浮点；未被计时的阶段值为 0 但仍输出键，
// 便于下游看到"这个阶段本轮发生了但耗时在时钟分辨率内"或"根本没发生"。
func emitTurnTimingLog(t *TurnTiming, stopped string) {
	snap := t.Snapshot()
	attrs := []any{
		slog.String("stopped", stopped),
		slog.Bool("complete", snap.complete),
		slog.Int64("total_us", snap.Total().Microseconds()),
	}
	for _, p := range turnTimingPhases {
		attrs = append(attrs, slog.Int64(string(p)+"_us", snap.phases[p].Microseconds()))
	}
	if len(snap.incompleteReasons) > 0 {
		attrs = append(attrs, slog.Any("incomplete_reasons", snap.incompleteReasons))
	}
	turnTimingLogger().Info("agent_turn_timing", attrs...)
}

// turnTimingBegin/turnTimingEnd 是各阶段计时的薄包装。opt-in 关闭（turnTiming 为 nil）
// 时是纯粹的空操作：不读时钟、不分配、不触碰任何状态。
func (s *runState) turnTimingBegin(p TurnPhase) {
	if s.turnTiming != nil {
		s.turnTiming.BeginPhase(p)
	}
}

func (s *runState) turnTimingEnd(p TurnPhase) {
	if s.turnTiming != nil {
		s.turnTiming.EndPhase(p)
	}
}

// finishTurnTiming 在一轮运行结束时标记完整性并落一次结构化日志。
// 关闭时整段是空操作；开启时无论何种停止原因都会落一条日志，
// 其中 complete/incomplete_reasons 与 Result.Stopped 保持一致。
func (s *runState) finishTurnTiming(stopped string) {
	if s.turnTiming == nil {
		return
	}
	if stopped == StopComplete {
		s.turnTiming.MarkComplete()
	} else {
		s.turnTiming.MarkIncomplete(stopped)
	}
	emitTurnTimingLog(s.turnTiming, stopped)
}

// toolTopSlowLimit 是轮末摘要日志展示的最慢工具数量。
const toolTopSlowLimit = 5

// emitToolTimingHistogramLog 把工具耗时直方图的最慢若干工具写成一条结构化日志，
// 与逐阶段计时日志共用同一个 logger（turnTimingLogger），便于统一捕获与路由。
// 直方图为 nil（opt-in 关闭）或本轮没有任何样本时整段是空操作。
func emitToolTimingHistogramLog(h *ToolTimingHistogram) {
	if h == nil {
		return
	}
	top := h.TopSlow(toolTopSlowLimit)
	if len(top) == 0 {
		return
	}
	// 每项用 P95 表达（与 TopSlow 的排序键一致），形如 "read:12ms,bash:8ms"。
	parts := make([]string, 0, len(top))
	for _, item := range top {
		parts = append(parts, item.Tool+":"+item.Stats.P95.String())
	}
	turnTimingLogger().Info("agent_tool_timing_histogram", slog.String("tool_top_slow", strings.Join(parts, ",")))
}

// finishToolTimingHistogram 在一轮运行结束时落一次最慢工具摘要日志。
// 关闭（直方图为 nil）时是空操作，与 turnTiming 是否开启无关。
func (s *runState) finishToolTimingHistogram() {
	emitToolTimingHistogramLog(s.toolTimingHistogram)
}

// emitToolCallTraceLog 把工具调用链摘要写成一条结构化日志，与逐阶段计时共用 logger。
// trace 为 nil（opt-in 关闭）时整段是空操作。
func emitToolCallTraceLog(tr *ToolCallTrace) {
	if tr == nil {
		return
	}
	turnTimingLogger().Info("agent_tool_call_trace", slog.String("summary", tr.Summary()))
}

// finishTrace 在一轮运行结束时落一次工具调用链摘要日志。
// 关闭（trace 为 nil）时是空操作，与 turnTiming/histogram 是否开启无关。
func (s *runState) finishTrace() {
	emitToolCallTraceLog(s.toolCallTrace)
}

// emitContextHealthLog 把一次上下文健康度评分写成一条结构化日志，与逐阶段计时共用
// 同一个 logger（turnTimingLogger），便于统一捕获与路由。字段固定输出，便于断言与比对：
// score 为综合健康分，guidance_repeat 为重复指引占比，error_stub 为错误占位占比。
func emitContextHealthLog(h ContextHealth) {
	turnTimingLogger().Info("agent_context_health",
		slog.Float64("score", h.Score),
		slog.Float64("guidance_repeat", h.GuidanceRepeatRatio),
		slog.Float64("error_stub", h.ErrorStubRatio),
	)
}

// emitPressureLog 把一次上下文压力分级写成一条结构化日志，与逐阶段计时/健康度日志
// 共用同一个 logger（turnTimingLogger），便于统一捕获与路由。level 为 PressureLabel
// 给出的中英双语标签，used/budget 为分级所用的累计用量与预算（均为 token 数）。
// 纯观测：不产生事件、不改消息、不改任何模型可见字节；字段固定输出，便于断言与比对。
func emitPressureLog(used, budget int) {
	level := ClassifyPressure(used, budget)
	turnTimingLogger().Info("agent_pressure",
		slog.String("level", PressureLabel(level)),
		slog.Int("used", used),
		slog.Int("budget", budget),
	)
}

// finishPressure 在一轮运行结束时落一条压力等级日志，与其它轮末日志并列。
// 关闭（pressureLog 为 false）时是空操作：不读取状态、不分级、不输出，与 turnTiming 是否开启无关。
// used 取累计用量 TotalTokens()，budget 取本运行的总 token 预算 MaxTotalTokens；
// 未配置预算（<=0）时 ClassifyPressure 归为 levelLow，语义为"不设限"。
func (s *runState) finishPressure() {
	if !s.pressureLog {
		return
	}
	emitPressureLog(s.usage.TotalTokens(), s.opts.Budget.MaxTotalTokens)
}

// contextHealthMessages 把当前消息样本投影为 ScoreContextHealth 所需的文本切片。
// 只取每条消息的 Content：带图消息的正文仍在 Content，图片走 ContentParts 旁路，
// 与文本健康度无关。只读取、不修改 s.messages 或其中的字符串。
func (s *runState) contextHealthMessages() []string {
	messages := make([]string, 0, len(s.messages))
	for _, m := range s.messages {
		messages = append(messages, m.Content)
	}
	return messages
}

// finishContextHealth 在一轮运行结束时投影当前消息样本并落一条健康度日志，
// 与其它轮末日志（finishTurnTiming/finishToolTimingHistogram/finishTrace）并列。
// 关闭（contextHealthLog 为 false）时是空操作：不采样、不输出，与 turnTiming 是否开启无关。
// 它纯观测：只用 ScoreContextHealth 这个纯函数读取消息，不改模型输入、裁剪或任何运行决策。
func (s *runState) finishContextHealth() {
	if !s.contextHealthLog {
		return
	}
	emitContextHealthLog(ScoreContextHealth(s.contextHealthMessages()))
}

// compactionPolicyInput 从轮次中的现有信号构造压缩策略输入。它是纯函数，供 opt-in
// 观测与测试共用：UsageRatio 由上下文估算值与压缩阈值相除得到（阈值非正时无可用预算信号，
// 记为 0），ErrorCount 取连续错误计数，ToolResults 取消息中的工具结果条数。
// BudgetExceeded 由调用方按当前预算是否已被突破传入。
func compactionPolicyInput(before, threshold, errors, toolResults int, budgetExceeded bool) CompactionInput {
	in := CompactionInput{
		ErrorCount:     errors,
		ToolResults:    toolResults,
		BudgetExceeded: budgetExceeded,
	}
	if threshold > 0 {
		in.UsageRatio = float64(before) / float64(threshold)
	}
	return in
}

// countToolResults 统计消息中的工具结果条数。角色按大小写与首尾空白不敏感归一化，
// 与 MeasureFocus 的工具结果判定一致；只读取消息，不修改内容，对 CJK 与任意多字节序列安全。
func countToolResults(messages []llm.Message) int {
	n := 0
	for _, m := range messages {
		switch strings.ToLower(strings.TrimSpace(m.Role)) {
		case "tool", "tool_result", "toolresult", "function":
			n++
		}
	}
	return n
}

// observeCompactionPolicy 在 opt-in 开启时把压缩策略决策作为辅助信号落一条日志。
// 它纯观测：只用纯函数 SelectCompactionAction 读取当前信号，不改消息、裁剪、压缩阈值、
// 压缩器调用或任何运行决策；关闭（compactionPolicy 为 false）时整段是空操作。
func (s *runState) observeCompactionPolicy() {
	if !s.compactionPolicy {
		return
	}
	before := EstimateMessages(s.messages)
	_, _, breached := s.budgetBreach()
	in := compactionPolicyInput(before, s.compactThreshold, s.mistakes.Count(), countToolResults(s.messages), breached)
	action := SelectCompactionAction(in)
	compactionPolicyLogger().Info("compaction policy decision",
		"action", string(action),
		"usage_ratio", in.UsageRatio,
		"error_count", in.ErrorCount,
		"tool_results", in.ToolResults,
		"budget_exceeded", in.BudgetExceeded,
		"turn", s.turns,
	)
}

// traceToolBegin 在 opt-in 开启时为一次调用开启跨度，并按调用 id 记下 spanID，
// 供随后 finishCall 配对结算（见 traceToolEnd）。关闭时是纯粹的空操作。
// 与 startTool 一样由 timingMu 保护映射，使并行批次写入不同 id 互不干扰。
func (s *runState) traceToolBegin(callID, tool string) {
	if s.toolCallTrace == nil {
		return
	}
	spanID := s.toolCallTrace.Begin(tool)
	s.timingMu.Lock()
	if s.toolCallSpans == nil {
		s.toolCallSpans = make(map[string]int)
	}
	s.toolCallSpans[callID] = spanID
	s.timingMu.Unlock()
}

// traceToolEnd 结算调用 id 对应的跨度；未知 id（重复结算/手工状态）是空操作。
// 关闭时是纯粹的空操作。spanID 取出后立即删除，避免跨轮次 id 复用时串账。
func (s *runState) traceToolEnd(callID string, ok bool, err error) {
	if s.toolCallTrace == nil {
		return
	}
	s.timingMu.Lock()
	spanID, found := s.toolCallSpans[callID]
	if found {
		delete(s.toolCallSpans, callID)
	}
	s.timingMu.Unlock()
	if found {
		s.toolCallTrace.End(spanID, ok, err)
	}
}

func newRunState(opts Options, prompt string, emit func(Event)) *runState {
	system := strings.TrimSpace(opts.System)
	if system == "" {
		root := opts.Root
		if root == "" {
			root = "."
		}
		system = fmt.Sprintf(systemTemplate, root)
	}
	// 根运行还没有 owner。向它的队列请求一个稳定的 owner：同一队列上的每次 Run
	// （包括 schema 重试或已恢复 Runner 的下一次提示）都解析为同一个 owner。
	// 不同的队列依据各自的计数器解析，绝不会与这一队列混合结果。
	// 子级已经带有由 childOptions 分配的 owner。
	if opts.bgOwner == 0 && opts.Subagent != nil && opts.Subagent.Background != nil {
		opts.bgOwner = opts.Subagent.Background.RootOwner()
	}
	s := &runState{
		opts:          opts,
		model:         opts.Model,
		tools:         newToolset(opts.Tools, opts),
		emitFn:        emit,
		rec:           normaliseRecorder(opts.Record),
		steer:         &steering{ch: opts.Steer},
		ttsr:          newTTSRWatcherForRun(opts.TTSRRules),
		beforeRequest: opts.BeforeRequest,
		beforeTool:    opts.BeforeTool,
		progress:      newProgressCapture(opts.ProgressSink, 1),
		guard:         newDedupe(repeatsFor(opts)),
		mistakes:      NewMistakeTracker(0, defaultMistakeGuidance),
		stuck:         &StuckDetector{},
		depth:         opts.Depth,
		bgOwner:       opts.bgOwner,
		maxTurns:      maxTurnsFor(opts),
		trim: TrimOptions{
			MaxTokens:         trimTarget(opts),
			KeepTurns:         opts.KeepTurns,
			KeepToolTokens:    opts.KeepToolTokens,
			TargetRatio:       defaultTrimTargetRatio,
			NoTrimToolCallIDs: opts.NoTrimToolCallIDs,
		},
		toolMeta:         make(map[string]ToolMeta),
		compactor:        opts.Compactor,
		compactThreshold: opts.CompactThreshold,
		costWatch:        NewCostWatcher(opts.Budget.MaxCostUSD),
		verifyState:      VerifyLoopState{Max: opts.MaxVerifyReflections, Root: opts.Root},
	}
	// 初始化提醒窗口，使第一次进入循环时不会被误判为新窗口
	// （那会在尚未消耗任何上下文时就在层级 0 告警）。
	if window, ok := s.budgetReminderWindow(); ok {
		s.reminderWindow = window
	}
	// 稳定系统提示：SystemNodes 非空时以 base 为前缀追加节点，保住 prompt cache 前缀；
	// 为空时不触碰 system，使默认路径逐字节不变。
	if len(opts.SystemNodes) > 0 {
		system = llm.StableSystemPrompt(system, opts.SystemNodes)
	}
	// 首条用户消息：仅在 opts.InitialImages 非空时构造带图 part 的消息；
	// 为 nil 时保持裸 {Role:"user", Content:prompt}，与接线之前逐字节相同。
	firstUser := llm.Message{Role: "user", Content: prompt}
	if len(opts.InitialImages) > 0 {
		firstUser = llm.BuildImageMessage(prompt, opts.InitialImages)
	}
	s.messages = []llm.Message{
		{Role: "system", Content: system},
		firstUser,
	}
	s.record("user", prompt)
	// 只有 ARGENT_TURN_TIMING=1 时才建实例；默认关闭时保持 nil，
	// 使下方各阶段的 Begin/End 与轮末日志全是空操作。
	if turnTimingEnabled() {
		s.turnTiming = &TurnTiming{}
	}
	// 只有 ARGENT_RECOVERY_METRICS=1 时才建账本；默认关闭时保持 nil，
	// 使下方各记录点与快照读取全是空操作。
	if recoveryMetricsEnabled() {
		s.recovery = &RecoveryMetrics{}
	}
	// 只有 ARGENT_TOOL_TIMING_HISTOGRAM=1 时才建直方图；默认关闭时保持 nil，
	// 使 dispatch 的 Record 与轮末摘要日志全是空操作。
	if toolTimingHistogramEnabled() {
		s.toolTimingHistogram = &ToolTimingHistogram{}
	}
	// 只有 ARGENT_TOOL_CALL_TRACE=1 时才建调用链追踪器；默认关闭时保持 nil，
	// 使 dispatch 的 Begin/End 与轮末摘要日志全是空操作。
	if toolCallTraceEnabled() {
		s.toolCallTrace = &ToolCallTrace{}
	}
	// 只有 ARGENT_CONTEXT_HEALTH_LOG=1 时才置位；默认关闭时保持 false，
	// 使轮末的消息投影与健康度日志全是空操作。
	if contextHealthLogEnabled() {
		s.contextHealthLog = true
	}
	// 只有 ARGENT_PRESSURE_LEVEL=1 时才置位；默认关闭时保持 false，
	// 使轮末的压力分级与日志全是空操作。
	if pressureLevelLogEnabled() {
		s.pressureLog = true
	}
	// 只有 ARGENT_COMPACTION_POLICY=1 时才置位；默认关闭时保持 false，
	// 使压缩触发点的策略观测整段是空操作，既有阈值路径逐字节不变。
	if compactionPolicyEnabled() {
		s.compactionPolicy = true
	}
	// 只有 ARGENT_TOKEN_FORECAST=1 时才置位并预分配用量历史；默认关闭时 usageHistory
	// 保持 nil，使 account() 的追加与请求前的预测分支都是零分配的空操作。
	if tokenForecastEnabled() {
		s.tokenForecast = true
		s.usageHistory = make([]int, 0, usageHistoryLimit)
	}
	return s
}

// runBeforeRequest 调用本次运行注入的请求屏障，并返回其第一个（也是唯一的）错误。
// nil 钩子是空操作，与未配置的运行一致。
func (s *runState) runBeforeRequest() error {
	if s.beforeRequest != nil {
		return s.beforeRequest()
	}
	return nil
}

// runBeforeTool 调用本次运行注入的工具屏障。nil 钩子是空操作。
func (s *runState) runBeforeTool() error {
	if s.beforeTool != nil {
		return s.beforeTool()
	}
	return nil
}

// trimTarget 确定裁剪所瞄准的估算提示大小。
func trimTarget(opts Options) int {
	target := opts.MaxContextTokens
	if target <= 0 {
		target = defaultContextTokens
	}
	if max := opts.Budget.MaxPromptTokens; max > 0 && max < target {
		target = max
	}
	return target
}

// setToolMeta 记录一次工具调用产生的元数据。调用方可能是并行批次中的 worker goroutine，
// 因此写入受互斥锁保护。
func (s *runState) setToolMeta(id string, meta ToolMeta) {
	if meta == (ToolMeta{}) {
		return
	}
	s.metaMu.Lock()
	s.toolMeta[id] = meta
	s.metaMu.Unlock()
}

// toolMetaSnapshot 为 Trim 复制目前已收集的元数据。调用方获得自己的映射，
// 因此 Trim 绝不会与仍在运行的批次发生竞态。
func (s *runState) toolMetaSnapshot() map[string]ToolMeta {
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	out := make(map[string]ToolMeta, len(s.toolMeta))
	for id, meta := range s.toolMeta {
		out[id] = meta
	}
	return out
}

// toolStepsFromEvents 把一次运行中已记录的 tool_end 事件投影为循环检测所需的步序列。
// 每个 tool_end 都带有工具名、参数与输出，无需与 tool_start 配对；未匹配的开始事件
// 被自然忽略。参数经规范化，使仅 JSON 键顺序不同的重复调用仍被视为同一步。
// 本函数只在循环检测 opt-in 开启时被调用。
func toolStepsFromEvents(events []Event) []ToolStep {
	out := make([]ToolStep, 0, len(events))
	for _, e := range events {
		if e.Kind != EventToolEnd {
			continue
		}
		out = append(out, ToolStep{
			Name:   e.ToolName,
			Input:  NormalizeArgs(e.Args),
			Output: e.Output,
		})
	}
	return out
}

// checkLoopDetection 在每个工具批次之后检查最近的工具调用是否构成循环。
// 默认关闭：SetLoopDetection 未开启时立即返回，不收集事件、不调用 DetectLoop，
// 也不触碰任何状态，因此消息、事件与会话记录逐字节不变。
//
// 触发时不硬停：复用 stuck/mistake 的 nudge 注入路径（injectSteeringWithNudge），
// 追加一条模型可读的用户轮次，让模型自行换一种方法恢复。提示按检测签名去重，
// 同一个循环只提示一次；一旦某步不再检出循环就清空去重状态，使后续新的循环
// 情节可以再次提示。
func (s *runState) checkLoopDetection() {
	if !LoopDetectionEnabled() {
		return
	}
	steps := toolStepsFromEvents(s.eventsSnapshot())
	if len(steps) == 0 {
		return
	}
	sig, _, detected := DetectLoop(steps, 0, 0)
	if !detected {
		// 没有循环意味着上一段循环情节已结束；清空去重状态，使下一段能重新提示。
		s.loopGuidance.Reset()
		return
	}
	if !s.loopGuidance.ShouldInject(sig) {
		return
	}
	s.injectSteeringWithNudge(loopDetectionGuidance)
}

// recordRecoveryAttempt 在失败指引实际注入时调用：账本登记一次尝试，并把 kind 记为
// 待恢复种类，使随后的成功信号有归属。opt-in 关闭（s.recovery == nil）时为空操作，
// 不触碰任何状态，因此消息与会话记录逐字节不变。
func (s *runState) recordRecoveryAttempt(kind string) {
	if s.recovery == nil {
		return
	}
	s.recovery.RecordAttempt(kind)
	s.recoveryPendingKind = kind
}

// recordRecoverySuccess 在 loop 侧观察到"无指引动作"（连续失败序列被工具成功清零）时调用：
// 若存在待恢复种类，把它标记为已恢复并清空待恢复状态。dispatch.go 的成功 Reset 无法在此层
// 直接调用，故用该信号等效替代。只有先前的尝试登记过 kind 才会记账，避免把序列尚未达到
// 上限的中间态误判为恢复。opt-in 关闭时为空操作。
func (s *runState) recordRecoverySuccess() {
	if s.recovery == nil || s.recoveryPendingKind == "" {
		return
	}
	s.recovery.RecordSuccess(s.recoveryPendingKind)
	s.recoveryPendingKind = ""
}

// recoverySnapshot 返回恢复成效账本的只读快照，供诊断观测。
// nil 接收方安全：opt-in 关闭时 s.recovery 为 nil，Snapshot 返回非 nil 空 map。
func (s *runState) recoverySnapshot() map[string]RecoveryStats {
	return s.recovery.Snapshot()
}

// hookContextNoticePrefix 标记一条由 hook 附加上下文变成的用户轮次，
// 与 steeringNoticePrefix 区分，使前端与测试无需解析正文即可识别它。
const hookContextNoticePrefix = "hook context: "

const (
	// hookContextMaxItems 限制一次运行注入的 hook 附加上下文条数，防止多条钩子堆积淹没上下文。
	hookContextMaxItems = 16
	// hookContextMaxBytes 限制一次运行注入的 hook 附加上下文总字节数。
	// 单条已由 hooks 侧限制到 8192 字节，这里留出约两条的空间以容纳多 hook 合并。
	hookContextMaxBytes = 2 * 8192
)

// HookContextQueue 承载 hook 返回的 additionalContext，在轮次边界被 agent 抽取为模型可见的用户轮次。
//
// 它由 app 层通过 WithHookContextQueue 附着到 ctx：队列在整次运行中存活且线程安全，
// 因此工具批次期间派发的 hook 也能把上下文交到下一个轮次边界。入队时按文本去重并限制条数与
// 总字节数，超出部分被静默丢弃；抽取按入队顺序进行，保证多 hook 合并稳定。
// 没有队列或队列为空时注入是空操作，默认运行逐字节不变。
type HookContextQueue struct {
	mu     sync.Mutex
	items  []string
	seen   map[string]bool
	nBytes int
}

// AddHookOutcomes 把一组 hook Outcome 的附加上下文按事件顺序入队。
// 它直接读取 Outcome.AdditionalContext 字段，使 hooks 与 agent 之间的契约保持显式。
func (q *HookContextQueue) AddHookOutcomes(outcomes []hooks.Outcome) {
	for _, o := range outcomes {
		if o.AdditionalContext != "" {
			q.Add(o.AdditionalContext)
		}
	}
}

// Add 把一条附加上下文入队：丢弃空白与重复文本，并受条数/总字节上限约束。
// 返回该条是否实际入队。超限只整条丢弃，绝不在 rune 中间切割，故 CJK 文本安全。
func (q *HookContextQueue) Add(text string) bool {
	if q == nil {
		return false
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.seen == nil {
		q.seen = make(map[string]bool, hookContextMaxItems)
	}
	if q.seen[text] {
		return false
	}
	if len(q.items) >= hookContextMaxItems || q.nBytes+len(text) > hookContextMaxBytes {
		return false
	}
	q.seen[text] = true
	q.items = append(q.items, text)
	q.nBytes += len(text)
	return true
}

// drain 按入队顺序取出并清空待注入项。seen 不清空，
// 使同一文本在整次运行中只注入一次。
func (q *HookContextQueue) drain() []string {
	if q == nil {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	items := q.items
	q.items = nil
	q.nBytes = 0
	return items
}

type hookContextKey struct{}

// WithHookContextQueue 把附加上下文队列附着到 ctx，供本轮 agent 在轮次边界抽取。
func WithHookContextQueue(ctx context.Context, q *HookContextQueue) context.Context {
	return context.WithValue(ctx, hookContextKey{}, q)
}

// HookContextQueueFrom 返回附着在 ctx 上的队列；没有时返回 nil。
func HookContextQueueFrom(ctx context.Context) *HookContextQueue {
	q, _ := ctx.Value(hookContextKey{}).(*HookContextQueue)
	return q
}

// injectHookContexts 把 hook 附加上下文作为用户轮次注入对话。
// 与 injectSteering 一样只在轮次边界运行，不会插进工具批次；没有队列或队列为空时是空操作。
func (s *runState) injectHookContexts(ctx context.Context) bool {
	texts := HookContextQueueFrom(ctx).drain()
	for _, text := range texts {
		s.messages = append(s.messages, llm.Message{Role: "user", Content: text})
		s.record("hook_context", text)
		s.emit(Event{Kind: EventNotice, Text: hookContextNoticePrefix + text})
	}
	return len(texts) > 0
}

// loop 持续运行各个轮次，直到模型不使用工具就作答、某个限制被触发，或上下文被取消。
func (s *runState) loop(ctx context.Context) (Result, error) {
	for {
		if err := ctx.Err(); err != nil {
			s.appendAbortedAssistant()
			s.emit(Event{Kind: EventStop, Reason: StopCancelled})
			return s.result(StopCancelled), err
		}
		s.turnTimingBegin(phaseUserWait)
		s.injectSteering()
		// hook 的 additionalContext 与 steering 一样在轮次边界注入，不会插进工具批次。
		// 没有队列时为逐字节空操作，默认运行不受影响。
		s.injectHookContexts(ctx)
		s.turnTimingEnd(phaseUserWait)
		// 在每次进入轮次时重新评估连续错误序列。没有可用指引意味着运行无法恢复，到此结束；
		// 否则恢复指令会成为一条新的用户轮次。对于没有追踪器构建的 runState，
		// 这两个判断都是 nil 安全的。
		if stop, action := s.mistakes.Check(); stop {
			s.emit(Event{Kind: EventStop, Reason: stopRepeatedMistakes})
			return s.result(stopRepeatedMistakes), nil
		} else if action.Guidance != "" {
			// 失败指引逐轮原样注入会淹没上下文：Trim 只裁剪工具消息，用户轮次会一直堆积到压缩。
			// opt-in 开启时以错误种类作为签名去重，同一次连续失败期间同一种类只注入一次；
			// 这里刻意不用 count——它每轮递增，会破坏去重。关闭（默认）时走原路径，每轮照常注入，行为逐字节不变。
			if !guidanceDedupeEnabled() || s.guidance.ShouldInject(string(action.Kind)) {
				s.messages = append(s.messages, llm.Message{Role: "user", Content: action.Guidance})
				s.record("mistake_guidance", nudgePayload(action.Guidance))
				// 仅统计：opt-in 关闭时为空操作，不改变指引注入或停止逻辑。
				s.recordRecoveryAttempt(string(action.Kind))
			}
		} else {
			// 没有指引动作意味着连续失败序列已被某次工具成功清零（或尚未开始）。
			// 这是 loop 侧可见的成功轨迹：dispatch.go 的成功 Reset 无法在此层直接调用，
			// 故用该信号等效替代，仅登记恢复成效，不改任何指引/停止行为。
			if guidanceDedupeEnabled() {
				// 清空去重状态，使下一段失败能重新注入同一种子指引；关闭 opt-in 时不触碰状态。
				s.guidance.Reset()
			}
			s.recordRecoverySuccess()
		}
		if s.turns >= s.maxTurns {
			// 取消可能是在循环的顶部轮次检查之后才到达的（例如在抽取控制消息时）。
			// 应像处理其他中止一样处理它，而不是当作干净的轮次限制停止。
			if err := ctx.Err(); err != nil {
				s.appendAbortedAssistant()
				s.emit(Event{Kind: EventStop, Reason: StopCancelled})
				return s.result(StopCancelled), err
			}
			s.emit(Event{Kind: EventNotice, Text: fmt.Sprintf("turn limit reached after %d turn(s)", s.turns)})
			// 请求一次无工具的答案，使耗尽的预算仍能产出有用的内容。停止原因保持为 StopMaxTurns，
			// 除非最后一次请求被取消——finalAnswer 会返回上下文错误，
			// 以免它被吞进一个成功的结果里。
			if !s.opts.NoFinalAnswer {
				if err := s.finalAnswer(ctx); err != nil {
					s.appendAbortedAssistant()
					s.emit(Event{Kind: EventStop, Reason: StopCancelled})
					return s.result(StopCancelled), err
				}
			}
			s.emit(Event{Kind: EventStop, Reason: StopMaxTurns})
			return s.result(StopMaxTurns), nil
		}
		if reason, detail, hit := s.budgetBreach(); hit {
			s.emit(Event{Kind: EventNotice, Text: detail})
			s.emit(Event{Kind: EventStop, Reason: reason})
			return s.result(reason), nil
		}

		// 每个轮次都重新读取 schema：某个工具可能已被上一次调用解锁，
		// 它必须对紧接着的下一次请求可见。可选过滤器会在工具被公布之前，
		// 隐藏策略或沙箱禁止的工具，使模型免于一次注定失败的调用。
		s.specs = s.advertisedSpecs()
		target := s.trim.MaxTokens
		if schemaTokens := EstimateTools(s.specs); schemaTokens > 0 && target > schemaTokens+1000 {
			target -= schemaTokens
		}
		trimOpts := s.trim
		trimOpts.MaxTokens = target
		trimOpts.ToolMeta = s.toolMetaSnapshot()
		if trimmed := Trim(s.messages, trimOpts); trimmed.Pruned > 0 {
			s.messages = trimmed.Messages
			s.emit(Event{
				Kind: EventNotice,
				Text: fmt.Sprintf("trimmed %d old tool result(s), ~%d tokens freed", trimmed.Pruned, trimmed.Freed),
			})
		}
		// 模型可见的预算提醒：当上下文占用率跨越新的里程碑时，注入一条简短的用户轮次，
		// 使模型能在窗口填满前收尾。此时所有停止检查都已通过、裁剪已运行，且记录是配对的轮末视图，
		// 因此这里的一条独立用户消息不会拆散 tool_use/tool_result 对。
		// 未配置的运行为空操作，消息保持逐字节相同。
		s.maybeBudgetReminder()

		// token_budget_forecast（ARGENT_TOKEN_FORECAST=1，默认关闭）：在请求前用最近若干轮的
		// 用量外推下一轮，若累计用量加上下一轮预测将触及总预算，就提前发提示并压缩一次。
		// 该点位于轮末视图（上一批次的 tool_use/tool_result 已配对）且在任何请求之前，
		// 绝不会把压缩插进工具调用与其结果之间。关闭时整段是零分配的空操作。
		// promptEstimate 在其后计算，因此会看到提前压缩后的历史。
		s.maybeForecastCompact()

		// 一次估算同时用于预算停止检查和 TurnStart 事件；两次使用之间没有任何东西修改消息。
		promptEstimate := EstimateMessages(s.messages) + EstimateTools(s.specs)

		if max := s.opts.Budget.MaxPromptTokens; max > 0 {
			if promptEstimate >= max {
				detail := fmt.Sprintf("%s: prompt estimate %d tokens (limit %d)", StopMaxPromptTokens, promptEstimate, max)
				s.emit(Event{Kind: EventNotice, Text: detail})
				s.emit(Event{Kind: EventStop, Reason: StopMaxPromptTokens})
				return s.result(StopMaxPromptTokens), nil
			}
		}

		turn := s.turns
		s.emit(Event{Kind: EventTurnStart, Turn: turn})
		// 持久性接缝：在开销较大的请求之前刷写会话。钩子错误会像其他聊天失败一样处理——
		// 不发起请求，运行以提供方错误结束，而不是冒着让存储区尚未准备好承受的工作丢失的风险。
		if err := s.runBeforeRequest(); err != nil {
			failure := llm.ClassifyFailure(err)
			s.emit(Event{Kind: EventError, Turn: turn, Err: err.Error(), Failure: &failure})
			s.emit(Event{Kind: EventStop, Reason: StopProviderError})
			return s.result(StopProviderError), err
		}
		// 为请求加上时间边界，使推理块能携带真实的墙上时钟区间。
		// 只有在回复确实包含推理时才会消费起点；否则下一轮次会直接覆盖它。
		s.startReasoning(timeSource())
		s.turnTimingBegin(phaseInference)
		reply, err := s.chatWithFallback(ctx, turn)
		s.turnTimingEnd(phaseInference)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				s.appendAbortedAssistant()
				s.emit(Event{Kind: EventStop, Reason: StopCancelled})
				return s.result(StopCancelled), ctxErr
			}
			failure := llm.ClassifyFailure(err)
			// 选择性的"压缩后重放"：默认关闭。只有 opt-in 开启、失败是上下文溢出且
			// 预算未耗尽时，compactAndReplay 才会压缩记录并在同一轮次重发请求。
			// 未发生重放时沿用下方的原有失败路径，事件与返回值保持不变。
			if next, nextErr, retried := s.compactAndReplay(ctx, turn, failure); retried {
				reply, err = next, nextErr
				if err == nil {
					// 压缩改变了提示大小：用重放时真正发出的记录重算估算，
					// 使随后 account() 的用量与预算检查基于新历史。
					promptEstimate = EstimateMessages(s.messages) + EstimateTools(s.specs)
				}
			}
			if err != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					s.appendAbortedAssistant()
					s.emit(Event{Kind: EventStop, Reason: StopCancelled})
					return s.result(StopCancelled), ctxErr
				}
				failure = llm.ClassifyFailure(err)
				s.emit(Event{Kind: EventError, Turn: turn, Err: err.Error(), Failure: &failure})
				s.emit(Event{Kind: EventStop, Reason: StopProviderError})
				return s.result(StopProviderError), err
			}
		}
		if reply.Reasoning != "" {
			// 推理通过 Chatter 作为一个整体块到达；单独流式输出推理的提供方在这里仍能正确渲染。
			timing := s.finishReasoning(timeSource())
			s.emit(Event{
				Kind:         EventReasoningDelta,
				Turn:         turn,
				Text:         reply.Reasoning,
				StartedAtMS:  timing.StartedAtMS,
				FinishedAtMS: timing.FinishedAtMS,
			})
		}
		turnUsage, promptTokens := s.account(reply, promptEstimate)
		s.promptTokens = promptTokens
		s.turns++
		s.messages = append(s.messages, llm.Message{
			Role:               "assistant",
			Content:            reply.Content,
			ToolCalls:          reply.ToolCalls,
			Reasoning:          reply.Reasoning,
			ReasoningSignature: reply.ReasoningSignature,
		})
		if reply.Content != "" {
			s.lastText = reply.Content
		}
		// 持久化原始回复：llm.Message 已经把 Reasoning 作为 JSON 元数据（"reasoning"）携带，
		// 因此会话记录保留了模型的思考文本，session.Entry 会在恢复时把它投影回来。
		s.record("assistant", reply)
		s.emit(Event{Kind: EventTurnEnd, Turn: turn})
		s.emit(Event{Kind: EventUsage, Turn: turn, Usage: turnUsage, TotalUsage: s.usage})

		// 只对结束情况分类一次：长度截断既影响下方的工具批次，也影响无工具的继续路径。
		// 每条未被截断的回复都会恢复继续预算。
		truncated := s.replyTruncated(reply)
		if truncated {
			s.consecutiveLengthFinishes++
		} else {
			s.consecutiveLengthFinishes = 0
		}

		// 可见答案或工具批次证明模型正在取得进展，因此会清除空回复序列。
		// 纯推理和真正的空回复则不会；下方的纠正路径正是为它们准备的。
		kind := ClassifyReply(reply)
		if strings.TrimSpace(reply.Content) != "" || len(reply.ToolCalls) > 0 {
			s.consecutiveEmptyReplies = 0
		}

		if len(reply.ToolCalls) == 0 {
			// 在答案流式输出期间到达的纠正消息仍值得一个答复：再开一轮，而不是结束。
			// hook 附加上下文同理：若在本轮期间到达，也让模型再看一轮而非直接结束。
			if s.injectSteering() || s.injectHookContexts(ctx) {
				continue
			}
			if truncated {
				// 答案不完整只是因为输出预算耗尽。在新轮次中请求剩余部分，
				// 但不要让卡在同一限制上的模型空转到轮次预算结束。
				if s.consecutiveLengthFinishes >= maxConsecutiveLengthFinishes {
					s.emit(Event{Kind: EventStop, Reason: StopOutputLimit})
					return s.result(StopOutputLimit), nil
				}
				s.messages = append(s.messages, llm.Message{Role: "user", Content: continueAfterOutputLimit})
				s.record("user", continueAfterOutputLimit)
				s.emit(Event{Kind: EventNotice, Text: "output limit reached; asking the model to continue in smaller steps"})
				continue
			}
			// 空回复或纯推理回复不是答案：给模型一次有限的机会产出可见内容，
			// 而不是把运行当作完成而结束。宣告了工具调用却未交付任何部分的结束情况属于降级场景——
			// 这里刻意让它落入停止分支，以免在无休止的提示循环里重发未改变的记录。
			if kind.NeedsCorrection() && !kind.IsDowngraded() &&
				s.consecutiveEmptyReplies < maxConsecutiveEmptyReplies {
				s.consecutiveEmptyReplies++
				s.injectSteering(emptyReplyCorrection)
				s.emit(Event{Kind: EventNotice, Text: "empty reply; asking the model for a visible answer"})
				continue
			}
			s.emit(Event{Kind: EventStop, Reason: StopComplete})
			return s.result(StopComplete), nil
		}

		// 在批次之前检查，使已被突破的预算不会浪费工具执行时间。
		if reason, detail, hit := s.budgetBreach(); hit {
			s.fillUnrun(reply.ToolCalls, "run stopped on its budget before this call executed")
			s.emit(Event{Kind: EventNotice, Text: detail})
			s.emit(Event{Kind: EventStop, Reason: reason})
			return s.result(reason), nil
		}

		if truncated {
			// 被长度截断的回复可能携带写了一半的工具参数。为每个调用配上错误结果，但一个都不执行。
			s.fillUnrun(reply.ToolCalls, truncatedToolReason)
			s.emit(Event{Kind: EventNotice, Text: truncatedToolReason})
		} else if err := s.runBeforeTool(); err != nil {
			// 持久性屏障失败：不得派发任何工具。用有序的错误结果回应每个调用，
			// 使记录中每个工具调用都与结果配对。
			reason := fmt.Sprintf("tool dispatch aborted: %v", err)
			s.fillUnrun(reply.ToolCalls, reason)
			s.emit(Event{Kind: EventNotice, Text: reason})
		} else {
			// 工具批次是独立计时阶段。只包住真正的 runTools 调用：
			// 上方的截断/fillUnrun 与 BeforeTool 屏障不派发工具，不应计入 tools 阶段。
			s.turnTimingBegin(phaseTools)
			stopped := s.runTools(ctx, turn, reply.ToolCalls)
			s.turnTimingEnd(phaseTools)
			if stopped != "" {
				if stopped == StopCancelled {
					// 批次已经追加了其（未运行的）工具结果；用一条终结助手消息收尾记录，
					// 使最后一条消息是中止，而非待处理的工具使用。
					s.appendAbortedAssistant()
				}
				s.emit(Event{Kind: EventStop, Reason: stopped})
				if stopped == StopCancelled {
					return s.result(stopped), ctx.Err()
				}
				return s.result(stopped), nil
			}
		}

		// ……之后再检查一次，因为工具可能耗时很久，而下一次请求才是开销较大的部分。
		if reason, detail, hit := s.budgetBreach(); hit {
			s.emit(Event{Kind: EventNotice, Text: detail})
			s.emit(Event{Kind: EventStop, Reason: reason})
			return s.result(reason), nil
		}

		// 一批工具结果是查找重复循环的天然位置。检测器每个情节只触发一次：
		// 卡住模式会变成一条纠正性的用户轮次，而不是硬性停止。
		if guidance, _ := s.stuck.Observe(s.eventsSnapshot()); guidance != "" {
			s.injectSteeringWithNudge(guidance)
		}

		// 工具调用循环检测（opt-in，默认关闭）：收集最近的工具调用步，若 DetectLoop
		// 判定为循环则注入一条模型可读的纠正提示（软提示，不硬停）。关闭时立即返回，
		// 不收集事件、不调用检测器，既有行为逐字节不变。
		s.checkLoopDetection()

		// 针对纯粹无进展循环的选择性提前停止：同一个调用持续返回同一个结果。
		// 默认关闭（Options.NoProgress == nil），因此未配置的运行行为逐字节不变。
		// 该检查在批次之后运行，使每个调用都有已记录的结果。
		if cfg := s.opts.NoProgress; cfg != nil {
			sigs := SignaturesFromEvents(s.eventsSnapshot())
			if stuck, reason := DetectNoProgressWithThreshold(sigs, cfg.Threshold); stuck {
				s.emit(Event{Kind: EventNotice, Text: reason})
				s.emit(Event{Kind: EventStop, Reason: StopStuck})
				return s.result(StopStuck), nil
			}
		}

		// 一批工具结果也是进行摘要的天然位置：下一次请求即将再次为整段历史付费。
		s.turnTimingBegin(phaseCompaction)
		// opt-in 压缩策略观测：仅在 ARGENT_COMPACTION_POLICY=1 时落一条决策日志。
		// 它不替换既有阈值路径——maybeCompact 仍按原逻辑决定是否压缩，两者共存。
		s.observeCompactionPolicy()
		s.maybeCompact()
		s.turnTimingEnd(phaseCompaction)

		// 有界的编辑后验证（默认关闭）。在压缩之后运行，使新注入的反馈轮次
		// 保持在下一次请求的末尾。
		if reason, stop := s.verifyAfterBatch(ctx, turn, reply.ToolCalls); stop {
			s.emit(Event{Kind: EventStop, Reason: reason})
			return s.result(reason), nil
		}
	}
}
