package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/security"
)

const (
	// truncatedToolReason 是当回复因长度原因被提供方结束时，为其携带的每个调用写入的错误结果：
	// 其 JSON 参数可能在 token 中途被截断，因此执行它可能会依据一个只指定了一半的请求行事。
	truncatedToolReason = "tool arguments may be truncated (finish reason: length)"

	// duplicateIDReason 是针对调用 id 不唯一或为空的批次所写的错误结果。
	// 没有稳定的 id，记录就无法把每个结果与对应的调用配对，因此其中任何调用都不能安全执行。
	duplicateIDReason = "duplicate or empty tool call ID"

	// toolProgressKey 是受门控的进度发出器在 context 中的键。该字面量与 internal/tools 中的
	// toolProgressKey 保持一致：按分层规则，tools 不能导入 agent，因此两个包无法共享类型化键。
	// 双方约定使用普通的 func(string) 值。
	toolProgressKey = "argent.tool.progress"

	// noopEditHint 是编辑工具报告"没有任何改动"时附加的 aider 风格提示，
	// 用于阻止模型重发完全相同的编辑。
	noopEditHint = "The change appears to be already applied. Do not re-send the same edit; only address remaining differences."

	// stopInvalidArguments 在无效参数熔断器跳闸时结束运行。
	// 它是 runTools 返回的内部停止原因，经由循环现有的通用停止路径传递；
	// 公开的 Stop* 常量和事件语义均不变（与 stopRepeatedMistakes 类似）。
	stopInvalidArguments = "invalid_arguments"
)

// noopEditMarkers 是编辑工具用来表示改动已存在的小写短语。
// 匹配刻意很窄（仅限编辑工具），因此其他所有工具的输出都保持逐字节不变。
var noopEditMarkers = []string{"already", "no changes", "unchanged", "already present", "already applied"}

// isNoopEditTool 报告 name 是否为这样一个编辑工具：其无操作结果应携带防重发提示。
func isNoopEditTool(name string) bool {
	switch name {
	case "edit", "multi_edit":
		return true
	default:
		return false
	}
}

// hasNoopEditMarker 报告 output 看起来是否像一次无操作的编辑报告。
func hasNoopEditMarker(output string) bool {
	lower := strings.ToLower(output)
	for _, marker := range noopEditMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// invalidArgMarkers 是工具拒绝调用参数时使用的错误文本片段。
// 工具错误以纯字符串形式跨过 ToolRunner 边界（runTool 包装 err.Error()），
// 因此分类必须基于文本；熔断器本身保持独立于 tools 和 i18n（见 mistake_tracker.go）。
// 该集合要保持窄：一个恰好包含这些片段的无关失败只会清除熔断器，
// 因此过度匹配会浪费熔断器的信号价值。
var invalidArgMarkers = []string{
	"bad arguments",     // 畸形 JSON —— 几乎所有内置工具都会发出
	"invalid arguments", // i18n error.invalid_args (en) + 技能/子 agent 工具
	"参数无效",              // i18n error.invalid_args (zh-CN)
}

// isInvalidArgError 报告工具错误文本是否表示调用参数被判定为无效，
// 而非无关的运行时失败。
func isInvalidArgError(msg string) bool {
	lower := strings.ToLower(msg)
	for _, marker := range invalidArgMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// augmentNoopEditOutput 追加一次提示，同时保留工具自身的报告不变。
// 它是幂等的，以防工具已经把提示回显出来。
func augmentNoopEditOutput(output string) string {
	if strings.Contains(output, noopEditHint) {
		return output
	}
	trimmed := strings.TrimRight(output, " \t\r\n")
	if trimmed == "" {
		return noopEditHint
	}
	return trimmed + "\n" + noopEditHint
}

// 单轮次批次的工具门控、派发和记账。
//
// 只读批次可以并发运行，但每个决定（重复防护、审批、排序）都在循环 goroutine 上做出，
// 且结果按调用顺序记录，因此记录内容绝不依赖于哪个工具先完成。

// finishReasonReporter 是可选接缝，Chatter 通过它暴露最近一次回复的提供方结束/停止原因。
//
// 局限：llm.Message 没有结束原因字段（internal/llm 中的线上客户端在组装消息时
// 丢弃了 finish_reason / stop_reason），而 Result.Stopped 是循环在运行后写入的自身停止原因，
// 不是逐条回复的提供方信号。两者都无法告诉循环某条回复是因长度被截断的。
// 只有真正解析了 "length"（或等价的 "max_tokens"）的客户端实现该接口，截断检测才会生效。
// 未实现它的 Chatter 保持原有行为：其调用会被正常派发。
type finishReasonReporter interface {
	FinishReason() string
}

// isLengthReason 报告提供方的结束原因是否表示输出长度被截断。
// 该列表覆盖所支持协议使用的各种写法。
func isLengthReason(reason string) bool {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "length", "max_tokens", "max_output_tokens", "max_tokens_reached", "model_length":
		return true
	default:
		return false
	}
}

// replyTruncated 报告刚到达的回复是否因提供方输出 token 耗尽而结束。
// 它读取线上客户端附加到 llm.Message 上的逐条回复结束原因，
// 当消息没有携带时回退到可选的 Chatter 接缝（供伪实现使用）。
func (s *runState) replyTruncated(reply llm.Message) bool {
	if isLengthReason(reply.FinishReason) {
		return true
	}
	reporter, ok := s.opts.Chatter.(finishReasonReporter)
	return ok && isLengthReason(reporter.FinishReason())
}

// hasInvalidCallIDs 报告批次中是否有 id 为空或重复。带有这种 id 的批次必须整体回应：
// 重复的 id 会使两条结果在记录中无法区分，而空的 id 根本无法与调用配对。
func hasInvalidCallIDs(calls []llm.ToolCall) bool {
	seen := make(map[string]bool, len(calls))
	for _, call := range calls {
		if strings.TrimSpace(call.ID) == "" || seen[call.ID] {
			return true
		}
		seen[call.ID] = true
	}
	return false
}

// progressGate 在拥有这些更新的工具调用返回后拒绝接收更新。
// 流式工具遗留的 goroutine 不得在最终结果已记录之后，再追加过时的记录行。
type progressGate struct {
	accepting atomic.Bool
}

// close 停止接收更新。它必须在 execute 返回后立即运行。
func (g *progressGate) close() { g.accepting.Store(false) }

// gate 包装 emit，使 close 之后触发的回调成为静默的空操作。
func (g *progressGate) gate(emit func(string)) func(string) {
	return func(text string) {
		if g.accepting.Load() && emit != nil {
			emit(text)
		}
	}
}

// withProgressGate 在 ctx 中安装一个进度发出器，并返回控制它的门控。
// 调用方必须在 execute 返回后立即关闭该门控。
func withProgressGate(ctx context.Context, emit func(string)) (context.Context, *progressGate) {
	gate := &progressGate{}
	gate.accepting.Store(true)
	return context.WithValue(ctx, toolProgressKey, gate.gate(emit)), gate
}

// breaker 返回本次运行的无效参数熔断器，采用延迟构造，使手工构建的 runState（例如测试中）
// 保持 nil 安全。runTools 会在任何并行 worker 观察到它之前，先在循环 goroutine 上初始化它。
func (s *runState) breaker() *InvalidArgBreaker {
	if s.invalidBreaker == nil {
		s.invalidBreaker = NewInvalidArgBreaker(0)
	}
	return s.invalidBreaker
}

// runTools 执行一个批次，仅当运行提前结束（取消或无效参数熔断器跳闸）时才返回停止原因。
func (s *runState) runTools(ctx context.Context, turn int, calls []llm.ToolCall) string {
	// 熔断器统计一个轮次内的无效参数：新一轮次会开启新的连续序列。
	// 在任何 worker 观察到计数器之前，先在循环 goroutine 上重置它。
	s.breaker().Reset()
	// 同时重置批次后验证信号：它描述的是*本*批次。一个已派发且含变更型调用的批次，
	// 即使重复防护阻断了其中部分调用，仍有资格接受验证；下方的整体拒绝会走 fillUnrun，
	// 它会清除该信号。
	s.batchVerificationEligible = s.batchHasMutation(calls)

	// 在创建检查点或做任何派发决定之前先做校验：一个错误的 id 会使整个批次失效，
	// 因此其中任何调用都不能被宣告为可运行。
	if hasInvalidCallIDs(calls) {
		s.fillUnrun(calls, duplicateIDReason)
		return ""
	}

	// 检查点刻意在对批次重排或任何防护阻断调用之前宣告：它描述的是模型原本的请求。
	if ctx.Err() == nil {
		if cp := s.newCheckpoint(turn, calls); cp != nil {
			s.emit(Event{Kind: EventCheckpointReady, Turn: turn, Checkpoint: cp})
			if s.opts.Checkpoint != nil {
				s.opts.Checkpoint(*cp)
			}
		}
	}
	// 对每个调用的参数只规范化一次。重复防护在这里（wouldBlock）和调用派发时（check）
	// 都会被查询；共享这张表可以省去每次查询完整的 JSON 解码/编码，
	// 而不改变防护器看到或记录的内容。
	normalized := make([]string, len(calls))
	for i, call := range calls {
		normalized[i] = NormalizeArgs(call.Function.Arguments)
	}

	parallel := len(calls) > 1 && s.maxParallel() > 1
	for i, call := range calls {
		// 变更型调用需要审批，委派工具拥有嵌套状态；两者都不适合放在 worker goroutine 上。
		// 被重复防护拒绝的调用也必须按调用顺序记录，因此这种情况下保持批次串行。
		if s.mutating(call.Function.Name) ||
			call.Function.Name == TaskToolName ||
			s.guard.wouldBlockNormalized(call.Function.Name, normalized[i]) {
			parallel = false
			break
		}
	}
	if parallel {
		return s.runParallelCalls(ctx, turn, calls, normalized)
	}

	for i, call := range calls {
		if ctx.Err() != nil {
			for j := i; j < len(calls); j++ {
				pending := calls[j]
				s.emitToolStart(turn, pending)
				s.finishCall(turn, pending, callResult{output: cancelledCallReason, isError: true})
				s.appendTool(pending, cancelledCallReason)
			}
			return StopCancelled
		}
		s.emitToolStart(turn, call)
		if hit, blocked := s.guard.checkNormalized(call.Function.Name, normalized[i]); hit {
			s.finishCall(turn, call, callResult{output: blocked, isError: true})
			s.appendTool(call, blocked)
			continue
		}
		res := s.invoke(ctx, call)
		// 把执行结果反馈给防护器，使输出签名能识别"不同参数、相同输出"的循环。
		// 被阻断的调用永远不会到达这一行，因此其错误文本不会被记录为工具输出。
		s.guard.recordOutput(call.Function.Name, normalized[i], res.output)
		s.finishCall(turn, call, res)
		s.appendTool(call, res.output)
	}
	if s.breaker().ShouldStop() {
		return stopInvalidArguments
	}
	return ""
}

// runParallelCalls 并发执行一个只读批次，同时保持记录按调用顺序排列。
// 所有调用都在任何 worker 启动之前宣告，结果只在整块完成后记录，
// 因此事件流和历史绝不依赖于哪个工具先返回。
func (s *runState) runParallelCalls(ctx context.Context, turn int, calls []llm.ToolCall, normalized []string) string {
	// 可选回执：只有调用方通过 WithBatchReceipt 显式安装发出器时才生成，
	// 因此默认路径的事件与历史记录逐字节不变。
	emitReceipt, _ := ctx.Value(batchReceiptEmitterKey{}).(func(string))
	var records []CallRecord
	if emitReceipt != nil {
		records = make([]CallRecord, 0, len(calls))
	}

	// 重复防护属于决策，因此在 worker 启动前按调用顺序运行；只有通过该检查的调用
	// 才会到达 goroutine。
	blocked := make([]string, len(calls))
	for i, call := range calls {
		if hit, message := s.guard.checkNormalized(call.Function.Name, normalized[i]); hit {
			blocked[i] = message
		}
	}
	for _, call := range calls {
		s.emitToolStart(turn, call)
	}
	results := make([]callResult, len(calls))
	size := s.maxParallel()
	for start := 0; start < len(calls); start += size {
		end := min(start+size, len(calls))
		var wg sync.WaitGroup
		for i := start; i < end; i++ {
			if blocked[i] != "" {
				continue
			}
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				// worker 只负责执行工具。错误追踪器和无效参数熔断器是连续计数器，
				// 其最终状态取决于观察顺序，因此它们在下方按调用顺序重放，
				// 而不是在这里改动。
				results[i] = s.executeCall(ctx, calls[i])
			}(i)
		}
		wg.Wait()
	}
	for i, call := range calls {
		res := results[i]
		if blocked[i] != "" {
			res = callResult{output: blocked[i], isError: true}
		} else {
			// 结果在循环 goroutine 上按调用顺序记录，因此尽管 worker 顺序不确定，
			// 防护器的输出窗口仍保持确定性。
			s.guard.recordOutput(call.Function.Name, normalized[i], res.output)
			// 连续计数器遵循同样的规则：恰好最后完成的成功调用，
			// 不得抹掉批次中排在它之前的失败。
			s.observeOutcome(res)
		}
		s.finishCall(turn, call, res)
		s.appendTool(call, res.output)
		if emitReceipt != nil {
			records = append(records, callRecordFrom(call, res))
		}
	}
	if emitReceipt != nil {
		emitReceipt(partialBatchReceipt(records))
	}
	if ctx.Err() != nil {
		return StopCancelled
	}
	if s.breaker().ShouldStop() {
		return stopInvalidArguments
	}
	return ""
}

// callResult 是一次工具调用产生的结果。
type callResult struct {
	output   string
	isError  bool
	duration time.Duration
	// intent 是在工具运行前从调用参数中剥离的简洁 `i` 字符串。它会体现在 tool_end 事件上。
	intent string
	// invalidArgs 标记该错误结果是由工具拒绝调用参数引起的。
	// 它为无效参数熔断器提供输入，而无需重新解析文本。
	invalidArgs bool
	// ran 报告该调用是否真正执行了工具。审批失败和拒绝会在派发前返回，
	// 对错误追踪器和无效参数熔断器必须保持中性。
	ran bool
}

// approvalDigest 返回对被请求审批的确切参数的稳定且有界的哈希。
// 它从不含原始参数，因此该通知可以安全地放进普通的记录流。
func approvalDigest(args string) string {
	sum := sha256.Sum256([]byte(args))
	return hex.EncodeToString(sum[:])
}

// invoke 对单个调用做门控并执行，然后把结果纳入本次运行的连续计数器。
// 它是串行派发和直接调用方的入口。并行 worker 必须使用 executeCall，
// 并让循环 goroutine 按调用顺序重放结果（见 runParallelCalls）。
func (s *runState) invoke(ctx context.Context, call llm.ToolCall) callResult {
	res := s.executeCall(ctx, call)
	s.observeOutcome(res)
	return res
}

// executeCall 对一次调用做门控并执行，但不触碰错误追踪器或无效参数熔断器。
// 两者都是连续计数器，其最终状态取决于观察顺序：如果并行 worker 各自记录结果，
// 那么最后完成的工具将决定某个连续序列是否保留。调用方必须在循环 goroutine 上，
// 用 observeOutcome 按调用顺序把结果重新纳入。
func (s *runState) executeCall(ctx context.Context, call llm.ToolCall) callResult {
	start := time.Now()
	// 受防护的工具集已经运行了安全链，它会询问同一个审批器。
	// 在循环中再次询问会使一次调用弹出第二个确认框。
	if guarded, ok := s.opts.Tools.(interface{ Guarded() bool }); !ok || !guarded.Guarded() {
		if s.needsApproval(call.Function.Name) {
			// 发出成对的通知事件，使每一种传输方式（Emit、--rpc、native）都能审计审批，
			// 而无需改动协议。这些只是事件：对于不消费它们的调用方，
			// 决定和由此产生的调用结果均不变。
			digest := approvalDigest(call.Function.Arguments)
			s.emit(ApprovalRequestedEvent(call.Function.Name, digest, call.ID))
			allowed, err := s.opts.Approve.Ask(ctx, call.Function.Name, call.Function.Arguments)
			s.emit(ApprovalResolvedEvent(call.Function.Name, call.ID, err == nil && allowed))
			switch {
			case err != nil:
				return callResult{
					output:   fmt.Sprintf("error: approval for %q failed: %v", call.Function.Name, err),
					isError:  true,
					duration: time.Since(start),
				}
			case !allowed:
				return callResult{
					output:   fmt.Sprintf("error: the operator denied %q; do not repeat this call without changing the request", call.Function.Name),
					isError:  true,
					duration: time.Since(start),
				}
			}
		}
	}
	res := s.runTool(ctx, call, start)
	res.ran = true
	return res
}

// observeOutcome 把一次已执行调用的结果纳入连续计数器。
// 它必须在循环 goroutine 上按调用顺序运行。未运行的调用（审批失败、重复防护阻断、
// 已取消的批次）带有 ran=false，并保持中性。
func (s *runState) observeOutcome(res callResult) {
	if !res.ran {
		return
	}
	// 真正运行且失败的工具会计入连续错误序列；成功调用会清除它，
	// 使该序列的含义是"没有进展"。
	if res.isError {
		s.mistakes.Record(MistakeKindToolExecutionFailed)
	} else {
		s.mistakes.Reset()
	}
	// 比错误序列更窄：只有参数校验被拒才会推进无效参数熔断器。
	// 成功和任何非参数失败都证明有进展，并清除它。
	outcome := InvalidArgOutcomeSuccess
	if res.invalidArgs {
		outcome = InvalidArgOutcomeInvalidArgs
	} else if res.isError {
		outcome = InvalidArgOutcomeOtherFailure
	}
	s.breaker().Observe(outcome)
}

// isRecoverablePlanBlock 报告 err 是否来自可选的计划模式命令防护。
// 这些是普通且可恢复的工具错误：runTool 会把它们写入记录，循环继续进行。
// 单纯的拒绝/StopError 不在此匹配，仍沿用原有处理。
func isRecoverablePlanBlock(err error) bool {
	return errors.Is(err, security.ErrRecoverablePlanBlock)
}

func (s *runState) runTool(ctx context.Context, call llm.ToolCall, start time.Time) callResult {
	// 流式工具可以通过 ctx 中的发出器发布中间行。门控只在 execute 返回前打开：
	// 遗留 goroutine 之后触发的任何回调都会被丢弃，而不会进入记录。
	turn := s.turns - 1
	if turn < 0 {
		turn = 0
	}
	ctx, gate := withProgressGate(ctx, func(text string) {
		s.emit(Event{Kind: EventNotice, Turn: turn, Text: text})
		s.record("tool_progress", map[string]string{
			"id": call.ID, "name": call.Function.Name, "text": text,
		})
	})
	defer gate.close()

	// 在工具看到参数之前剥离模型的简洁意图。没有 `i` 时清理后的字节与原值相同，
	// 因此对所有现有工具调用的行为都不变。
	intent, cleaned := ExtractIntent(json.RawMessage(call.Function.Arguments))
	output, meta, err := s.callTool(ctx, call.Function.Name, string(cleaned))
	s.setToolMeta(call.ID, meta)
	if err != nil {
		// 工具错误以文本形式跨过运行器边界，因此在原始错误仍可用的这里进行分类。
		// 熔断器据此把反复出现的参数校验失败与无关失败区分开。
		invalid := isInvalidArgError(err.Error())
		if isRecoverablePlanBlock(err) {
			// 计划模式命令防护的阻断是可恢复的工具错误：运行会继续且模型能读到指引，
			// 与硬性的 StopError 拒绝不同。
			return callResult{intent: intent, output: "error: " + err.Error() + " Continue with a read-only command or a different approach.", isError: true, invalidArgs: invalid, duration: time.Since(start)}
		}
		return callResult{intent: intent, output: "error: " + err.Error(), isError: true, invalidArgs: invalid, duration: time.Since(start)}
	}
	if isNoopEditTool(call.Function.Name) && hasNoopEditMarker(output) {
		// 无操作编辑是模型即将重复自身的强烈信号。该提示会成为模型下一步看到的工具结果的一部分。
		output = augmentNoopEditOutput(output)
	}
	return callResult{intent: intent, output: output, duration: time.Since(start)}
}

func (s *runState) emitToolStart(turn int, call llm.ToolCall) {
	// 开始事件在工具运行之前发出，此时还没有 callResult；
	// 因此直接从调用参数中读取意图。
	intent, _ := ExtractIntent(json.RawMessage(call.Function.Arguments))
	// 按调用 id 记录墙上时钟起点。finishCall 会消费它，使 tool_end 携带一个
	// 在会话重载后仍然有效的绝对区间。
	started := timeSource()
	s.startTool(call.ID, started)
	s.emit(Event{
		Kind:        EventToolStart,
		Turn:        turn,
		ToolCallID:  call.ID,
		ToolName:    call.Function.Name,
		Args:        call.Function.Arguments,
		Intent:      intent,
		StartedAtMS: unixMillis(started),
	})
}

func (s *runState) finishCall(turn int, call llm.ToolCall, res callResult) {
	// 与 emitToolStart 对每次调用（正常、被阻断、已取消和并行）成对出现。
	// 未知 id 是安全的空操作，相关字段保持缺省。
	timing := s.finishTool(call.ID, timeSource())
	s.emit(Event{
		Kind:         EventToolEnd,
		Turn:         turn,
		ToolCallID:   call.ID,
		ToolName:     call.Function.Name,
		Args:         call.Function.Arguments,
		Intent:       res.intent,
		Output:       res.output,
		IsError:      res.isError,
		DurationMS:   res.duration.Milliseconds(),
		StartedAtMS:  timing.StartedAtMS,
		FinishedAtMS: timing.FinishedAtMS,
	})
	s.record("tool", map[string]string{
		"id":        call.ID,
		"name":      call.Function.Name,
		"arguments": call.Function.Arguments,
		"output":    res.output,
		"is_error":  fmt.Sprint(res.isError),
	})
}

func (s *runState) appendTool(call llm.ToolCall, output string) {
	s.messages = append(s.messages, llm.Message{
		Role:       "tool",
		ToolCallID: call.ID,
		Content:    output,
	})
}

// fillUnrun 为永远不会运行的每个调用写入错误结果，使记录中每个工具调用都与结果配对。
func (s *runState) fillUnrun(calls []llm.ToolCall, reason string) {
	// 该批次中的每个调用都未执行就得到回应，因此没有发生编辑，必须跳过编辑后验证。
	s.batchVerificationEligible = false
	turn := s.turns - 1
	if turn < 0 {
		turn = 0
	}
	for _, call := range calls {
		s.emitToolStart(turn, call)
		s.finishCall(turn, call, callResult{output: reason, isError: true})
		s.appendTool(call, reason)
	}
}
