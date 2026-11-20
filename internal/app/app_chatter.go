package app

import (
	"context"
	"errors"
	"net"
	"regexp"
	"strconv"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

func newChatter(protocol, baseURL, apiKey string, opts llm.Options) llm.Chatter {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "anthropic", "anthropic-messages", "messages":
		client := llm.NewAnthropic(baseURL, apiKey)
		client.MaxTokens = llm.DefaultAnthropicMaxTokens
		client.Options = opts
		return client
	case "responses", "openai-responses":
		client := llm.NewResponses(baseURL, apiKey)
		client.Options = opts
		return client
	default:
		client := llm.New(baseURL, apiKey)
		client.Options = opts
		return client
	}
}

// fallbackChatter retries once with --fallback-model when the primary model
// fails in a retryable way. Cancellation and deterministic provider errors are
// never retried. Each attempt's streamed text is buffered until it succeeds, so
// a model that emits half an answer and then fails cannot splice that partial
// text into the fallback's reply (the agent layer follows the same rule).
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

// chatBuffered holds a model's deltas until its request succeeds, then forwards
// them in order. A failed attempt's deltas are dropped when the caller switches
// to another model; without this, the operator would see the failed model's
// text concatenated with the successful model's answer.
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

// fallbackStatusRE finds an HTTP status in a provider error string such as
// "provider returned 429: ..." or "HTTP 503: service unavailable".
var fallbackStatusRE = regexp.MustCompile(`\b([1-5][0-9]{2})\b`)

// statusCodeInFallbackError extracts the first HTTP status from a provider
// error message. It reports false when the text carries no plausible status.
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

// isRetryableFallbackError decides whether another model can plausibly serve
// the same turn. It mirrors agent.isRetryableProviderError: llm owns transport
// failures (ClassifyRetryError) and HTTP statuses (IsRetryableStatus), with the
// legacy prose vocabulary unioned in for providers that report a retryable
// condition without a typed error or numeric status. Cancellation, validation
// failures and hard quota/billing limits stay final because repeating the same
// messages with another model cannot fix them.
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
		// A hard quota can arrive with a retryable-looking 429/402; llm owns
		// the distinction between a transient rate limit and a spent budget.
		if llm.IsNonRetryableQuotaResponse(code, []byte(lower)) {
			return false
		}
		return llm.IsRetryableStatus(code) || legacyRetryable
	}
	switch llm.ClassifyRetryError(err) {
	case llm.ReasonTimeout, llm.ReasonUnreachable:
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	return legacyRetryable
}

// containsAnyFallback reports whether text holds any of the lower-case needles.
func containsAnyFallback(text string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}
