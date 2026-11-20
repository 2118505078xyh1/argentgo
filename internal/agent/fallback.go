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

// modelAttempts resolves the ordered model list for a single turn. The first
// entry of FallbackModels is the primary; an empty field keeps the legacy
// Options.Model behaviour so callers written before wave 4 need no changes.
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

// chatWithFallback issues the turn's model request, walking the fallback list
// when the provider fails in a retryable way. Retries re-send the exact same
// message slice: nothing is appended to the transcript until this returns an
// answer, so a switch can never duplicate a user turn or a tool result.
func (s *runState) chatWithFallback(ctx context.Context, turn int) (llm.Message, error) {
	models := modelAttempts(s.opts)
	// The configured list is the hard ceiling; the +1 is the legacy
	// single-model attempt and keeps the bound explicit.
	maxAttempts := 1 + len(s.opts.FallbackModels)
	live := len(models) == 1 // no fallback configured: keep streaming as before

	var (
		lastErr error
		tried   []string
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
		onDelta := func(delta string) {
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
		if ctx.Err() != nil || !isRetryableProviderError(err) || i == len(models)-1 {
			break
		}
		next := models[i+1]
		// A provider switch is the retry signal the agent can see on its own:
		// per-attempt backoff lives inside llm.RetryPolicy and never reaches
		// this layer. Publish the switch as a structured retry so a frontend can
		// show progress, and keep the legacy notice untouched for old consumers.
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

// httpStatusRE finds an HTTP status code in a provider error string such as
// "provider returned 429: ..." or "HTTP 503: service unavailable". The status
// is the only reliable typed signal those errors carry, so it is handed to
// llm.IsRetryableStatus instead of being compared against local constants.
var httpStatusRE = regexp.MustCompile(`\b([1-5][0-9]{2})\b`)

// statusCodeInError extracts the first HTTP status from a provider error
// message. It reports false when the text carries no plausible status code.
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

// isRetryableProviderError decides whether another model can plausibly serve
// the same turn. llm.ClassifyRetryError owns transport failures and
// llm.IsRetryableStatus owns HTTP status codes; the decisions here are their
// union with the legacy prose vocabulary, so a provider that reports a
// retryable condition without a typed error or a numeric status keeps working.
// Cancellation and validation failures stay final because repeating the same
// messages cannot fix them.
func isRetryableProviderError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	lower := strings.ToLower(err.Error())
	// Validation errors cannot be fixed by repeating the same messages.
	if containsAny(lower, "invalid request", "validation", "bad request", "unauthorized", "forbidden", "permission denied") {
		return false
	}

	// Keep the legacy prose vocabulary: some providers describe the condition
	// without a typed error or a parseable status ("quota exceeded", "5xx").
	legacyRetryable := containsAny(lower,
		"429", "too many requests", "rate limit", "quota", "overloaded", "try again",
		"500", "502", "503", "504", "5xx",
		"internal server error", "bad gateway", "service unavailable", "gateway timeout",
		"connection reset", "connection refused", "broken pipe", "temporary failure",
		"timeout", "timed out",
	)

	// A numeric status means the provider answered, so the status is the more
	// specific signal. llm owns which statuses are retryable; the prose signal
	// is unioned in so an unusual status paired with "quota" keeps its old
	// decision.
	if code, ok := statusCodeInError(lower); ok {
		return llm.IsRetryableStatus(code) || legacyRetryable
	}

	// Without a status, llm classifies the transport failure. Timeouts and
	// unreachable hosts are the classes worth another model; a bare
	// ReasonNotSent (an ordinary application error) is deliberately not, since
	// repeating the same request is unlikely to change its answer.
	switch llm.ClassifyRetryError(err) {
	case llm.ReasonTimeout, llm.ReasonUnreachable:
		return true
	}

	// The legacy matcher switched models on any failed network operation,
	// including causes the typed classifier cannot name. Keep that case so the
	// decision stays the union of the old and new signals.
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	return legacyRetryable
}

// retryReason reduces a retryable provider error to one fixed, wire-safe token.
// It never copies provider text: the error may carry a credential, and even a
// redacted body does not belong on a progress badge. The tokens are the llm
// retry classes when a typed signal exists, "status" for a retryable HTTP code,
// and "not_sent" as a conservative default.
func retryReason(err error) string {
	if err == nil {
		return string(llm.ReasonNone)
	}
	if code, ok := statusCodeInError(strings.ToLower(err.Error())); ok && llm.IsRetryableStatus(code) {
		return string(llm.ReasonStatus)
	}
	if reason := llm.ClassifyRetryError(err); reason != llm.ReasonNone {
		return string(reason)
	}
	return string(llm.ReasonNotSent)
}

// containsAny reports whether text holds any of the lower-case needles.
func containsAny(text string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}
