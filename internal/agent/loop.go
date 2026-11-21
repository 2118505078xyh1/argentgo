package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

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

	usage        llm.Usage
	costUSD      float64
	costKnown    bool
	costWatch    *CostWatcher
	promptTokens int
	turns        int
	depth        int
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
		beforeRequest: opts.BeforeRequest,
		beforeTool:    opts.BeforeTool,
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
	s.messages = []llm.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: prompt},
	}
	s.record("user", prompt)
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

// loop 持续运行各个轮次，直到模型不使用工具就作答、某个限制被触发，或上下文被取消。
func (s *runState) loop(ctx context.Context) (Result, error) {
	for {
		if err := ctx.Err(); err != nil {
			s.appendAbortedAssistant()
			s.emit(Event{Kind: EventStop, Reason: StopCancelled})
			return s.result(StopCancelled), err
		}
		s.injectSteering()
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
			}
		} else if guidanceDedupeEnabled() {
			// 没有指引动作意味着连续失败序列已被某次工具成功清零（或尚未开始）。
			// 这是 loop 侧可见的成功轨迹：借此清空去重状态，使下一段失败能重新注入同一种子指引。
			// dispatch.go 的成功 Reset 无法在此层直接调用，故用该信号等效替代；关闭 opt-in 时不触碰状态。
			s.guidance.Reset()
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
		reply, err := s.chatWithFallback(ctx, turn)
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
			Role:      "assistant",
			Content:   reply.Content,
			ToolCalls: reply.ToolCalls,
			Reasoning: reply.Reasoning,
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
			if s.injectSteering() {
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
		} else if stopped := s.runTools(ctx, turn, reply.ToolCalls); stopped != "" {
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
		s.maybeCompact()

		// 有界的编辑后验证（默认关闭）。在压缩之后运行，使新注入的反馈轮次
		// 保持在下一次请求的末尾。
		if reason, stop := s.verifyAfterBatch(ctx, turn, reply.ToolCalls); stop {
			s.emit(Event{Kind: EventStop, Reason: reason})
			return s.result(reason), nil
		}
	}
}
