package app

import (
	"context"
	"errors"
	"net"
	"regexp"
	"strconv"
	"strings"

	"example.com/argentgo/internal/llm"
)

// newChatter 按协议名查工厂表构造客户端：协议名先做小写与首尾空白归一化，
// 未注册的协议回退到默认工厂（OpenAI 兼容的 chat completions）。
func newChatter(protocol, baseURL, apiKey string, opts llm.Options) llm.Chatter {
	key := strings.ToLower(strings.TrimSpace(protocol))
	factory, ok := chatterFactories[key]
	if !ok {
		factory = chatterFactories[defaultChatterProtocol]
	}
	return factory(baseURL, apiKey, opts)
}

// fallbackChatter 在主模型以可重试的方式失败时，用 --fallback-model 重试一次。
// 取消和确定性的提供方错误从不重试。每次尝试的流式文本会缓冲到该次尝试成功为止，
// 因此输出半个答案后失败的模型，其部分文本不会拼接进回退模型的回复
// （agent 层遵循同样的规则）。
type fallbackChatter struct {
	primary  llm.Chatter
	fallback string
}

func (f *fallbackChatter) Chat(
	ctx context.Context,
	model string,
	messages []llm.Message,
	tools []llm.ToolSpec,
	onText func(string),
) (llm.Message, error) {
	reply, err := f.chatBuffered(ctx, model, messages, tools, onText)
	if err == nil || ctx.Err() != nil || strings.TrimSpace(f.fallback) == "" || !isRetryableFallbackError(err) {
		return reply, err
	}
	return f.chatBuffered(ctx, f.fallback, messages, tools, onText)
}

// ChatRaw 把字节回放透传给底层客户端（若其实现 RawChatter）。
// 刻意不做 fallback 重试：快照 body 里的 model 字段是固定的，切换 fallback
// 模型却发送旧 body 会造成请求模型与声明不一致。回放失败时由 agent 清空
// replay 状态并回退到普通 Chat，由那里正常走 fallback 链。
func (f *fallbackChatter) ChatRaw(ctx context.Context, model string, body []byte, onText func(string)) (llm.Message, error) {
	raw, ok := f.primary.(llm.RawChatter)
	if !ok {
		return llm.Message{}, errors.New("fallback chatter: primary does not support raw replay")
	}
	var buffered []string
	reply, err := raw.ChatRaw(ctx, model, body, func(delta string) {
		buffered = append(buffered, delta)
	})
	if err != nil || onText == nil {
		return reply, err
	}
	for _, delta := range buffered {
		onText(delta)
	}
	return reply, nil
}

// chatBuffered 保存某个模型的增量，直到其请求成功，然后按顺序转发。
// 调用方切换到另一个模型时，失败尝试的增量会被丢弃；没有这一步，操作人员就会看到
// 失败模型的文本与成功模型的答案拼接在一起。
func (f *fallbackChatter) chatBuffered(
	ctx context.Context,
	model string,
	messages []llm.Message,
	tools []llm.ToolSpec,
	onText func(string),
) (llm.Message, error) {
	var buffered []string
	reply, err := f.primary.Chat(ctx, model, messages, tools, func(delta string) {
		buffered = append(buffered, delta)
	})
	if err != nil || onText == nil {
		return reply, err
	}
	for _, delta := range buffered {
		onText(delta)
	}
	return reply, nil
}

// fallbackStatusRE 从提供方错误字符串中查找 HTTP 状态码，例如
// "provider returned 429: ..." 或 "HTTP 503: service unavailable"。
var fallbackStatusRE = regexp.MustCompile(`\b([1-5][0-9]{2})\b`)

// statusCodeInFallbackError 从提供方错误消息中提取第一个 HTTP 状态码。
// 文本中没有看似合理的状态码时返回 false。
func statusCodeInFallbackError(text string) (int, bool) {
	match := fallbackStatusRE.FindStringSubmatch(text)
	if match == nil {
		return 0, false
	}
	code, err := strconv.Atoi(match[1])
	if err != nil {
		return 0, false
	}
	return code, true
}

// isRetryableFallbackError 判断另一个模型是否可能服务同一轮次。它对应 agent.isRetryableProviderError：
// 传输失败由 llm 负责（ClassifyRetry），HTTP 状态码由 llm 负责（IsRetryableStatus），
// 并并入旧版文本词汇，以照顾那些在没有类型化错误或数字状态码时报告可重试状况的提供方。
// 取消、校验失败以及硬性配额/计费限制保持为最终结论，因为换一个模型重发相同的消息无法修复它们。
func isRetryableFallbackError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	lower := strings.ToLower(err.Error())
	if containsAnyFallback(lower,
		"invalid request", "validation", "bad request",
		"unauthorized", "forbidden", "permission denied",
	) {
		return false
	}
	legacyRetryable := containsAnyFallback(lower,
		"429", "too many requests", "rate limit", "quota", "overloaded", "try again",
		"500", "502", "503", "504", "5xx",
		"internal server error", "bad gateway", "service unavailable", "gateway timeout",
		"connection reset", "connection refused", "broken pipe", "temporary failure",
		"timeout", "timed out",
	)
	if code, ok := statusCodeInFallbackError(lower); ok {
		// 硬性配额可能以看似可重试的 429/402 出现；瞬时限流与预算耗尽之间的区别由 llm 掌管。
		if llm.IsNonRetryableQuotaResponse(code, []byte(lower)) {
			return false
		}
		return llm.IsRetryableStatus(code) || legacyRetryable
	}
	switch llm.ClassifyRetry(err, 0, nil) {
	case llm.ReasonTimeout, llm.ReasonUnreachable:
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	return legacyRetryable
}

// containsAnyFallback 报告 text 是否包含任意一个小写 needle。
func containsAnyFallback(text string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}
