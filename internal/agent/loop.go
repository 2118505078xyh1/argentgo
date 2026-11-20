package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
			MaxTokens:      trimTarget(opts),
			KeepTurns:      opts.KeepTurns,
			KeepToolTokens: opts.KeepToolTokens,
			TargetRatio:    defaultTrimTargetRatio,
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
			s.messages = append(s.messages, llm.Message{Role: "user", Content: action.Guidance})
			s.record("mistake_guidance", action.Guidance)
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
			s.emit(Event{Kind: EventError, Turn: turn, Err: err.Error(), Failure: &failure})
			s.emit(Event{Kind: EventStop, Reason: StopProviderError})
			return s.result(StopProviderError), err
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
			s.injectSteering(guidance)
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

// maybeCompact 实现轮次中的压缩边界。它只在工具批次之后运行，绝不会在工具调用与其结果之间运行。
// 目标窗口是 0.8 迟滞带减去摘要预留量，因此一次压缩就能缓解压力，而不是在下一次检查时再次触发。
func (s *runState) maybeCompact() {
	if s.compactor == nil || s.compactThreshold <= 0 {
		return
	}
	before := EstimateMessages(s.messages)
	if before <= s.compactThreshold {
		return
	}
	window := int(float64(s.compactThreshold)*compactionRecoveryBand) - summaryReserveTokens
	if window < minCompactWindow {
		window = minCompactWindow
	}
	compacted, changed := s.compactor.MaybeCompact(s.messages, window)
	if !changed || len(compacted) == 0 {
		return
	}
	s.messages = compacted
	// 摘要后的上下文是一个新窗口：清除按窗口划分的投递状态，
	// 使预算提醒能随着该窗口被填满而再次触发。
	s.resetBudgetReminder()
	// 现在重新估算，使下一次迭代顶部的预算检查看到压缩后的历史，而非旧的超长历史。
	s.promptTokens = EstimateMessages(s.messages)
	s.emit(Event{Kind: EventNotice, Text: fmt.Sprintf(
		"compacted context: ~%d -> ~%d tokens (target %d)", before, s.promptTokens, window,
	)})
}

// advertisedSpecs 确定下一次请求的工具列表。没有过滤器时使用运行器的完整目录；
// 有过滤器时只公布允许的子集。授权仍然发生在 Run 中，绝不在这里。
// 最终列表在发送之前会针对当前模型的 schema 特性做投影。
func (s *runState) advertisedSpecs() []llm.ToolSpec {
	specs := s.tools.Specs()
	if s.opts.FilterSpecs != nil {
		specs = s.opts.FilterSpecs(specs)
	}
	return projectSpecsForModel(specs, s.model)
}

// projectSpecsForModel 对每个非空的 Parameters 文档应用按模型系列的 schema 投影。
// 返回的切片始终是新的副本，因此投影请求绝不会修改工具集自身的规格目录。
func projectSpecsForModel(specs []llm.ToolSpec, model string) []llm.ToolSpec {
	compat := llm.CompatNone
	name := strings.ToLower(model)
	switch {
	case strings.Contains(name, "moonshot"), strings.Contains(name, "kimi"):
		compat = llm.CompatMoonshot
	case strings.Contains(name, "gemini"):
		compat = llm.CompatGemini
	}
	if compat == llm.CompatNone {
		return specs
	}

	out := make([]llm.ToolSpec, len(specs))
	copy(out, specs)
	for i := range out {
		raw := out[i].Function.Parameters
		if len(raw) == 0 {
			continue
		}
		projected := llm.ProjectToolSchema(string(raw), compat)
		if projected != string(raw) {
			out[i].Function.Parameters = json.RawMessage(projected)
		}
	}
	return out
}

// chat 用当前模型发起一次模型请求，并把答案增量转发给 onText。
// 事件的发出留给调用方，使失败的尝试可以重试，而不会把部分文本泄漏到操作人员的流中。
func (s *runState) chat(ctx context.Context, onText func(string)) (llm.Message, error) {
	var streamed strings.Builder
	// 修复交给提供方的记录*视图*：在恢复或批次中断之后，助手的 tool_use 可能丢失了结果，
	// 严格的提供方会拒绝这种情况。NormalizeToolPairing 是纯视图辅助函数，
	// 因此 s.messages 保持原始字节，只有外发的请求被修正。
	messages := NormalizeToolPairing(s.messages)
	reply, err := s.opts.Chatter.Chat(ctx, s.model, messages, s.specs, func(delta string) {
		if delta == "" {
			return
		}
		streamed.WriteString(delta)
		if onText != nil {
			onText(delta)
		}
	})
	if err != nil {
		return llm.Message{}, err
	}
	if reply.Role == "" {
		reply.Role = "assistant"
	}
	if reply.Content == "" && streamed.Len() > 0 {
		// 只做流式输出却忘记组装的客户端，仍应把操作人员已看到的文本写入记录。
		reply.Content = streamed.String()
	}
	return reply, nil
}

// 下面的两个负载类型镜像了 session.UsageRecord 和 session.SessionStats 的线上结构，
// 但不导入 session 包：agent 层保持无依赖。它们通过注入的 Recorder 写入，
// app 把 Recorder 接到 Session.Append。保持 JSON 字段名同步，
// 才能让 session.UsageRecords / LatestStats / AggregateStats 读回它们。
type sessionUsagePayload struct {
	Model  string `json:"model,omitempty"`
	Input  int    `json:"input"`
	Output int    `json:"output"`
	Cached int    `json:"cached,omitempty"`
	TS     string `json:"ts,omitempty"`
}

type sessionStatsPayload struct {
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	CachedTokens     int     `json:"cached_tokens,omitempty"`
	Calls            int     `json:"calls"`
	CostUSD          float64 `json:"cost_usd,omitempty"`
	CostSource       string  `json:"cost_source,omitempty"`
	Updated          string  `json:"updated,omitempty"`
}

// recordRunStats 把一次运行的累计 token/花费汇总持久化为一条 stats 记录。
// 每次运行一条记录，可使 session.AggregateStats 的求和操作语义在多次恢复后仍然正确；
// 逐次请求的事实由 account() 写入。
func (s *runState) recordRunStats() {
	source := ""
	if s.costKnown {
		// 花费由注入的价格表推导而来，从不由提供方线上协议报告，因此它始终是估算值。
		source = "estimated"
	}
	s.record("stats", sessionStatsPayload{
		PromptTokens:     s.usage.PromptTokens,
		CompletionTokens: s.usage.CompletionTokens,
		CachedTokens:     s.usage.CachedTokens,
		Calls:            s.turns,
		CostUSD:          s.costUSD,
		CostSource:       source,
		Updated:          time.Now().UTC().Format(time.RFC3339),
	})
}

// account 把一条回复的用量折算进运行总量并返回它。
//
// 提供方报告的用量优先；否则使用刚发送的提示和收到的答案的估算值，
// 这样即使客户端完全不报告用量，预算也能工作。
func (s *runState) account(reply llm.Message, promptEstimate int) (llm.Usage, int) {
	var usage llm.Usage
	if reply.Usage != nil {
		usage = *reply.Usage
	}
	if usage.PromptTokens <= 0 {
		usage.PromptTokens = promptEstimate
	}
	if usage.CompletionTokens <= 0 {
		usage.CompletionTokens = EstimateTokens(reply.Content)
	}
	s.usage.PromptTokens += usage.PromptTokens
	s.usage.CompletionTokens += usage.CompletionTokens
	if price, ok := PriceFor(s.opts.Prices, s.model); ok {
		s.costUSD += price.Cost(usage)
		s.costKnown = true
	}
	// 每次运行一个观察器：随着花费累积，每个花费阈值只告警一次。
	for _, warning := range s.costWatch.Observe(s.costUSD) {
		s.emit(Event{Kind: EventNotice, Text: warning})
	}
	// 逐次请求的用量事实。在轮次中途写入（在任何终结助手记录之前），
	// 因此它绝不会改变 recorder 最后看到的是哪一行。
	s.record("usage", sessionUsagePayload{
		Model:  s.model,
		Input:  usage.PromptTokens,
		Output: usage.CompletionTokens,
		Cached: usage.CachedTokens,
		TS:     time.Now().UTC().Format(time.RFC3339),
	})
	return usage, usage.PromptTokens
}

// injectSteering 把排队的纠正消息移入对话。额外的文本（例如一次性的卡住提示）
// 也走同一条用户轮次路径。
func (s *runState) injectSteering(extra ...string) bool {
	pending := s.steer.drain()
	// 把已完成的后台子 agent 结果作为合成用户轮次抽取到本次运行中。
	// 投递以本次运行的唯一 owner 为键，而非深度：同级子 agent 共享相同深度，
	// 否则会窃取彼此的结果。深度保留在每个排队项上仅用于诊断。
	// 见 SubagentConfig.Background / BackgroundResults。
	if s.opts.Subagent != nil && s.opts.Subagent.Background != nil {
		pending = append(pending, s.opts.Subagent.Background.DrainFor(s.bgOwner)...)
	}
	pending = append(pending, extra...)
	for _, text := range pending {
		s.messages = append(s.messages, llm.Message{Role: "user", Content: text})
		s.record("steer", text)
		s.emit(Event{Kind: EventNotice, Text: "steering: " + text})
	}
	return len(pending) > 0
}

// budgetReminderPrefix 在记录中标记一条模型可见的预算提醒，
// 使测试和前端无需解析英文正文即可识别它。
const budgetReminderPrefix = "[budget reminder] "

// budgetReminderWindow 返回用于提醒的上下文占用窗口，以及本次运行是否启用提醒。
// 提醒以压缩阈值作为选择性启用的条件，它是循环管理的显式上下文边界：
// 未配置阈值的运行不改动其消息，因此默认行为（包括仅裁剪的 MaxContextTokens 路径）不变。
func (s *runState) budgetReminderWindow() (int, bool) {
	if s.compactThreshold > 0 {
		return s.compactThreshold, true
	}
	return 0, false
}

// resetBudgetReminder 在压缩后清除按窗口划分的投递状态。下一次检查会把摘要后的上下文
// 当作一个新窗口，并可以再次播报其层级。
func (s *runState) resetBudgetReminder() {
	s.reminderDelivered = nil
	s.reminderWindow = 0
}

// maybeBudgetReminder 在当前上下文占用率跨越新里程碑时，向 s.messages 追加一条模型可见的提醒。
// 它以只读方式复用纯决策层（BudgetTier/ShouldRemind），对没有配置窗口的运行是空操作。
//
// 该提醒是一条独立的 "user" 轮次，与控制/错误指引注入器类似：调用方在循环顶部、
// 停止检查和裁剪之后、记录处于配对的轮末视图时调用它。这个位置保证了消息会在下一次请求中
// 真正被发送（模型会读到它并自行收尾），同时不会拆散 tool_use/tool_result 对，
// 也不会改变任何停止语义。
func (s *runState) maybeBudgetReminder() {
	window, ok := s.budgetReminderWindow()
	if !ok || window <= 0 {
		return
	}
	if s.reminderDelivered == nil {
		s.reminderDelivered = make(map[int]bool)
	}
	// 不同的窗口大小（压缩后全新的 run state，或配置发生变化）意味着新窗口；
	// ShouldRemind 随后会清除投递记录，并允许相同层级再次告警。
	newWindow := s.reminderWindow != window
	s.reminderWindow = window

	curTier := BudgetTier(s.promptTokens, window)
	// delivered 恰好保存已播报的层级，因此其最大值就是决策层应比较的上一层级。
	prevTier := 0
	for tier := range s.reminderDelivered {
		if tier > prevTier {
			prevTier = tier
		}
	}
	if !ShouldRemind(prevTier, curTier, s.reminderDelivered, newWindow) {
		return
	}

	text := budgetReminderPrefix + BudgetReminderText(window-s.promptTokens, window)
	s.messages = append(s.messages, llm.Message{Role: "user", Content: text})
	s.emit(Event{Kind: EventBudgetReminder, Turn: s.turns, Text: text})
}

// maxParallel 是只读批次的 worker 数量。
func (s *runState) maxParallel() int {
	if s.opts.MaxParallel <= 1 {
		return 1
	}
	return s.opts.MaxParallel
}

// mutating 报告某工具是否会改变状态、因而需要审批。
func (s *runState) mutating(name string) bool {
	if s.opts.Mutating != nil {
		return s.opts.Mutating(name)
	}
	if len(s.opts.MutatingTools) > 0 {
		for _, candidate := range s.opts.MutatingTools {
			if candidate == name {
				return true
			}
		}
		return false
	}
	for _, candidate := range defaultMutatingTools {
		if candidate == name {
			return true
		}
	}
	return false
}

func (s *runState) needsApproval(name string) bool {
	return s.opts.Approve != nil && s.mutating(name)
}

func (s *runState) budgetBreach() (string, string, bool) {
	return s.opts.Budget.Breach(s.usage, s.promptTokens, s.costUSD, s.costKnown)
}

// result 把本次运行快照为公开的 Result 类型。
func (s *runState) result(stopped string) Result {
	return Result{
		Text:     s.lastText,
		Messages: append([]llm.Message(nil), s.messages...),
		Usage:    s.usage,
		Turns:    s.turns,
		Stopped:  stopped,
	}
}

// appendAbortedAssistant 在被取消的运行上留下一条终结的助手轮次。
// 没有它，从最后一条助手消息推断结果的消费者会看到一个工具使用轮次（或根本没有助手消息），
// 而非中止。只有记录发生变化：Result.Stopped 仍为 StopCancelled。
func (s *runState) appendAbortedAssistant() {
	aborted := llm.Message{
		Role:         "assistant",
		Content:      "Operation aborted",
		FinishReason: "aborted",
	}
	s.messages = append(s.messages, aborted)
	s.record("assistant", aborted)
}

// emit 投递一个事件，打上子 agent 深度标记，并把文本增量镜像到旧版 Out 写入器。
//
// 记录与投递被刻意分离：eventsMu 只保护追加操作，
// 因此缓慢的 emitFn（管道写入、SSE 刷新、UI 渲染）绝不会阻塞进度回调或轮次边界的记录。
// 投递由追加顺序票号门控，使 emitFn 观察到的事件顺序与追加顺序完全一致，即使有多个并发发出者。
func (s *runState) emit(event Event) {
	event.Depth = s.depth
	// 前端可能想显示"N 条排队"的边界事件会携带控制消息积压的快照。
	// 这是非抽取式的读取，因此报告数量绝不会消费排队的纠正消息。
	// 其他所有事件种类保持零值。Pending 是 nil 安全的，因此没有 steering 的 runState 也没问题。
	switch event.Kind {
	case EventTurnStart, EventTurnEnd, EventStop:
		if s.steer != nil {
			event.PendingMessages = s.steer.Pending()
		}
	}
	// sync.Once 使 emit 在未经 newRunState 构建的 runState 上仍可用。
	s.emitOnce.Do(func() { s.emitCond = sync.NewCond(&s.emitSendMu) })

	s.eventsMu.Lock()
	seq := s.emitSeq
	s.emitSeq++
	s.events = append(s.events, event)
	s.eventsMu.Unlock()

	s.emitSendMu.Lock()
	for s.emitDelivered != seq {
		s.emitCond.Wait()
	}
	if event.Kind == EventTextDelta && s.opts.Out != nil {
		_, _ = io.WriteString(s.opts.Out, event.Text)
	}
	if s.emitFn != nil {
		s.emitFn(event)
	}
	s.emitDelivered++
	s.emitCond.Broadcast()
	s.emitSendMu.Unlock()
}

// eventsSnapshot 在 eventsMu 下复制已记录的事件。调用方获得自己的切片，
// 因此 worker 之后的追加不会改变它们观察到的内容。
func (s *runState) eventsSnapshot() []Event {
	s.eventsMu.Lock()
	defer s.eventsMu.Unlock()
	return append([]Event(nil), s.events...)
}

// record 持久化一条记录项。recorder 失败绝不会停止运行。
func (s *runState) record(kind string, payload any) {
	if s.rec == nil {
		return
	}
	s.recordMu.Lock()
	_ = s.rec.Append(kind, payload)
	s.recordMu.Unlock()
}
