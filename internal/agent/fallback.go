package agent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/2118505078xyh1/argent-go/internal/llm"
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

// isRetryableProviderError decides whether another model can plausibly serve
// the same turn. Provider clients return plain errors, so this combines
// errors.As for typed network failures with conservative text matching for
// HTTP status lines. Context cancellation and validation failures are final.
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
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	return containsAny(lower,
		"429", "too many requests", "rate limit", "quota", "overloaded", "try again",
		"500", "502", "503", "504", "5xx",
		"internal server error", "bad gateway", "service unavailable", "gateway timeout",
		"connection reset", "connection refused", "broken pipe", "temporary failure",
		"timeout", "timed out",
	)
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
