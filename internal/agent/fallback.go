package agent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// streamOnceChatter 是 Chatter 的可选扩展：能以非流式方式再取一次完整答复。
// 恢复调用刻意不接收 onText，因此不会把失败尝试已经推送过的前缀重复发给 UI。
type streamOnceChatter interface {
	ChatOnce(ctx context.Context, model string, messages []llm.Message, tools []llm.ToolSpec) (llm.Message, error)
}

// streamRecoveryReason 是流中断回补成功后发布的 EventRetry 原因标记。
// RetryInfo.Reason 是封闭的标量字符串，不携带任何提供方文本。
const streamRecoveryReason = "stream_recovery"

// capturedText 只读一次尝试已捕获的流式文本；Snapshot 的停止原因在这里无关紧要。
func capturedText(c *partialCapture) string {
	return c.Snapshot("").Text
}

// modelAttempts 解析单轮次的有序模型列表。FallbackModels 的第一项是主模型；
// 字段为空时保留旧版 Options.Model 行为，这样在 wave 4 之前编写的调用方无需改动。
func modelAttempts(opts Options) []string {
	if len(opts.FallbackModels) > 0 {
		out := make([]string, 0, len(opts.FallbackModels))
		for _, model := range opts.FallbackModels {
			model = strings.TrimSpace(model)
			if model != "" {
				out = append(out, model)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return []string{opts.Model}
}

// chatWithFallback 发起本轮次的模型请求，当提供方以可重试的方式失败时，
// 依次尝试回退列表。重试会重新发送完全相同的消息切片：在返回答案之前不会向记录追加任何内容，
// 因此切换模型绝不会重复用户轮次或工具结果。
func (s *runState) chatWithFallback(ctx context.Context, turn int) (llm.Message, error) {
	models := modelAttempts(s.opts)
	// 配置的列表是硬上限；+1 对应旧版单模型尝试，使上限显式可见。
	maxAttempts := 1 + len(s.opts.FallbackModels)
	live := len(models) == 1 // 未配置回退：像以前一样保持流式输出

	var (
		lastErr error
		tried   []string
		// recoveryTried 把非流式回补限制为每轮最多一次：即使回退链上有多个模型
		// 都以流错误失败，也绝不会为同一轮重复发起第二个非流式请求。
		recoveryTried bool
	)
	for i, model := range models {
		if i >= maxAttempts {
			break
		}
		if err := ctx.Err(); err != nil {
			return llm.Message{}, err
		}
		tried = append(tried, model)
		s.model = model

		var buffered []string
		// 无论处于实时还是缓冲模式，都捕获本次尝试的流。成功时丢弃该捕获；
		// 对运行最终报告为失败的那次尝试，会以附加的 EventPartial 形式发布它，
		// 这样真实的半截答案不会在失败边界处丢失。
		capture := newPartialCapture(model, turn, defaultPartialByteLimit)
		onDelta := func(delta string) {
			capture.Add(delta)
			// 进度写侧（opt-in）：默认 sink 为 nil 时 Add/SetTurn 是空操作。
			s.progress.SetTurn(turn)
			s.progress.Add(delta)
			if live {
				s.emit(Event{Kind: EventTextDelta, Turn: turn, Text: delta})
				return
			}
			buffered = append(buffered, delta)
		}
		reply, err := s.chat(ctx, onDelta)
		if err == nil {
			for _, delta := range buffered {
				s.emit(Event{Kind: EventTextDelta, Turn: turn, Text: delta})
			}
			return reply, nil
		}
		lastErr = err
		// 流中断回补（默认关闭）：只在 opt-in、错误确实是流中断、context 仍有效、
		// 失败尝试已经流式输出过文本、且 Chatter 具备非流式能力时触发。
		// 默认关闭时整段短路为一次 bool 读取，请求体与事件逐字节不变。
		if !recoveryTried && s.opts.EnableStreamRecovery && errors.Is(err, llm.ErrStreamError) &&
			ctx.Err() == nil && capturedText(capture) != "" {
			if once, ok := s.opts.Chatter.(streamOnceChatter); ok {
				recoveryTried = true
				recovered, onceErr := once.ChatOnce(ctx, model, NormalizeToolPairing(s.messages), s.specs)
				if onceErr == nil {
					// 失败尝试的缓冲增量整段丢弃：完整答复以 ChatOnce 的结果为准。
					buffered = nil
					s.emitRecoveredReply(turn, recovered, capture, live)
					s.emit(Event{
						Kind: EventRetry,
						Turn: turn,
						Retry: &RetryInfo{
							Attempt:     i + 2,
							MaxAttempts: maxAttempts + 1,
							Reason:      streamRecoveryReason,
							Model:       model,
						},
					})
					return recovered, nil
				}
			}
		}
		cancelled := ctx.Err() != nil
		if cancelled || !isRetryableProviderError(err) || i == len(models)-1 {
			// 这一次就是运行要报告的尝试。把提供方在失败前流式输出的内容呈现出来，
			// 以便 UI 显示真实的部分文本；将被重试的可重试失败不发出任何内容，
			// 因为最终答案归下一次尝试所有。
			s.emitPartial(capture.Snapshot(partialStopReason(cancelled)))
			break
		}
		next := models[i+1]
		// 切换提供方是 agent 自己能看到的重试信号：每次尝试的退避位于 llm.RetryPolicy 内部，
		// 不会到达这一层。把切换作为结构化重试发布，便于前端显示进度，
		// 同时为旧消费者保留原有通知不变。
		s.emit(Event{
			Kind: EventRetry,
			Turn: turn,
			Retry: &RetryInfo{
				Attempt:     i + 2,
				MaxAttempts: len(models),
				Reason:      retryReason(err),
				Model:       next,
			},
		})
		s.emit(Event{Kind: EventNotice, Text: fmt.Sprintf("model %s failed (%v); switching to %s", model, err, next)})
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("agent: no model configured")
	}
	if ctx.Err() != nil {
		return llm.Message{}, lastErr
	}
	return llm.Message{}, fmt.Errorf("agent: all %d model(s) failed (%s): %w", len(tried), strings.Join(tried, ", "), lastErr)
}

// emitRecoveredReply 发布非流式回补得到的完整答复。
// 实时模式下失败流的前缀已经推送给 UI，所以只发布精确后缀；前缀不匹配时宁可一个增量也不发，
// 避免同一段文本出现两次。缓冲模式下失败尝试的增量已被丢弃，因此发布整个答复。
func (s *runState) emitRecoveredReply(turn int, reply llm.Message, capture *partialCapture, live bool) {
	if live {
		prefix := capturedText(capture)
		if strings.HasPrefix(reply.Content, prefix) {
			if suffix := strings.TrimPrefix(reply.Content, prefix); suffix != "" {
				s.emit(Event{Kind: EventTextDelta, Turn: turn, Text: suffix})
			}
		}
		return
	}
	if reply.Content != "" {
		s.emit(Event{Kind: EventTextDelta, Turn: turn, Text: reply.Content})
	}
}

// httpStatusRE 从提供方错误字符串中查找 HTTP 状态码，例如
// "provider returned 429: ..." 或 "HTTP 503: service unavailable"。状态码是这类错误
// 携带的唯一可靠类型化信号，因此交给 llm.IsRetryableStatus 判断，而不是与本地常量比较。
var httpStatusRE = regexp.MustCompile(`\b([1-5][0-9]{2})\b`)

// statusCodeInError 从提供方错误消息中提取第一个 HTTP 状态码。
// 文本中没有看似合理的状态码时返回 false。
func statusCodeInError(text string) (int, bool) {
	match := httpStatusRE.FindStringSubmatch(text)
	if match == nil {
		return 0, false
	}
	code, err := strconv.Atoi(match[1])
	if err != nil {
		return 0, false
	}
	return code, true
}

// isRetryableProviderError 判断另一个模型是否可能服务同一轮次。
// 传输失败由 llm.ClassifyRetry 负责，HTTP 状态码由 llm.IsRetryableStatus 负责；
// 这里的判断是它们与旧版文本词汇的并集，因此即使提供方在没有类型化错误或数字状态码时
// 报告可重试状况，也仍然能正常工作。取消和校验失败保持为最终结果，因为重复相同的消息无法修复它们。
func isRetryableProviderError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	lower := strings.ToLower(err.Error())
	// 校验错误无法通过重复相同的消息来修复。
	if containsAny(lower, "invalid request", "validation", "bad request", "unauthorized", "forbidden", "permission denied") {
		return false
	}

	// 保留旧版文本词汇：有些提供方在没有类型化错误或可解析状态码时
	// 也会描述这种状况（"quota exceeded"、"5xx"）。
	legacyRetryable := containsAny(lower,
		"429", "too many requests", "rate limit", "quota", "overloaded", "try again",
		"500", "502", "503", "504", "5xx",
		"internal server error", "bad gateway", "service unavailable", "gateway timeout",
		"connection reset", "connection refused", "broken pipe", "temporary failure",
		"timeout", "timed out",
	)

	// 数字状态码意味着提供方已作答，因此它是更具体的信号。哪些状态码可重试由 llm 掌管；
	// 文本信号也被并入并集，这样与 "quota" 搭配的异常状态码仍保留旧有的判断。
	if code, ok := statusCodeInError(lower); ok {
		return llm.IsRetryableStatus(code) || legacyRetryable
	}

	// 没有状态码时，由 llm 对传输失败分类。超时和不可达主机是值得换模型的类别；
	// 单纯的 ReasonNotSent（普通应用错误）刻意不包括在内，
	// 因为重复相同请求不太可能改变其结果。
	switch llm.ClassifyRetry(err, 0, nil) {
	case llm.ReasonTimeout, llm.ReasonUnreachable:
		return true
	}

	// 旧版匹配器在任何网络操作失败时都会切换模型，包括类型化分类器无法命名的原因。
	// 保留该分支，使判断始终是新旧信号的并集。
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	return legacyRetryable
}

// retryReason 把可重试的提供方错误归纳为一个固定、可安全上线传输的标记。
// 它绝不复制提供方文本：错误中可能带有凭据，即使经过脱敏的正文也不该出现在进度徽标上。
// 有类型化信号时标记取 llm 的重试类别，可重试 HTTP 状态码取 "status"，
// 保守的默认值为 "not_sent"。
func retryReason(err error) string {
	if err == nil {
		return string(llm.ReasonNone)
	}
	if code, ok := statusCodeInError(strings.ToLower(err.Error())); ok && llm.IsRetryableStatus(code) {
		return string(llm.ReasonStatus)
	}
	if reason := llm.ClassifyRetry(err, 0, nil); reason != llm.ReasonNone {
		return string(reason)
	}
	return string(llm.ReasonNotSent)
}

// containsAny 报告 text 是否包含任意一个小写 needle。
func containsAny(text string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}
