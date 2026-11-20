package app

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// fallbackScriptChatter is a fake Chatter that records the models it was asked
// to use and drives each call from a script. No network or real model is used.
type fallbackScriptChatter struct {
	calls  []string
	script func(call int, model string, onText func(string)) (llm.Message, error)
}

func (c *fallbackScriptChatter) Chat(_ context.Context, model string, _ []llm.Message, _ []llm.ToolSpec, onText func(string)) (llm.Message, error) {
	c.calls = append(c.calls, model)
	return c.script(len(c.calls), model, onText)
}

func emit(text string, onText func(string)) {
	for _, chunk := range strings.SplitAfter(text, "-") {
		if chunk != "" {
			onText(chunk)
		}
	}
}

// TestFallbackChatterDropsPartialPrimaryText pins bug #1: a primary model that
// streams a half-answer and then fails must not leak that text into the
// fallback model's answer.
func TestFallbackChatterDropsPartialPrimaryText(t *testing.T) {
	primary := "PARTIAL-FROM-PRIMARY-"
	backup := "FULL-ANSWER-FROM-FALLBACK"
	inner := &fallbackScriptChatter{script: func(call int, model string, onText func(string)) (llm.Message, error) {
		switch call {
		case 1:
			if model != "primary" {
				t.Fatalf("first model = %q, want primary", model)
			}
			emit(primary, onText)
			return llm.Message{}, errors.New("HTTP 429: too many requests")
		case 2:
			if model != "backup" {
				t.Fatalf("second model = %q, want backup", model)
			}
			emit(backup, onText)
			return llm.Message{Role: "assistant", Content: backup}, nil
		default:
			t.Fatalf("unexpected call %d", call)
			return llm.Message{}, nil
		}
	}}
	chatter := &fallbackChatter{primary: inner, fallback: "backup"}

	var shown strings.Builder
	reply, err := chatter.Chat(context.Background(), "primary", nil, nil, func(delta string) {
		shown.WriteString(delta)
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if reply.Content != backup {
		t.Fatalf("reply = %q, want %q", reply.Content, backup)
	}
	if got := shown.String(); got != backup {
		t.Fatalf("streamed text = %q, want exactly %q (partial primary text leaked)", got, backup)
	}
}

// TestFallbackChatterReplaysSuccessfulPrimaryText keeps the success path: when
// the primary succeeds, its deltas are forwarded and no fallback request is
// made, even though a fallback model is configured.
func TestFallbackChatterReplaysSuccessfulPrimaryText(t *testing.T) {
	answer := "ANSWER-FROM-PRIMARY"
	inner := &fallbackScriptChatter{script: func(call int, _ string, onText func(string)) (llm.Message, error) {
		if call != 1 {
			t.Fatalf("unexpected fallback call %d", call)
		}
		emit(answer, onText)
		return llm.Message{Role: "assistant", Content: answer}, nil
	}}
	chatter := &fallbackChatter{primary: inner, fallback: "backup"}

	var shown strings.Builder
	if _, err := chatter.Chat(context.Background(), "primary", nil, nil, func(delta string) {
		shown.WriteString(delta)
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if got := shown.String(); got != answer {
		t.Fatalf("streamed text = %q, want %q", got, answer)
	}
	if len(inner.calls) != 1 {
		t.Fatalf("requests = %d, want 1 (successful primary must not retry)", len(inner.calls))
	}
}

// TestFallbackChatterRetriesOnlyRetryableErrors pins bug #2: deterministic
// provider errors are returned immediately while transient failures still
// switch to the fallback model.
func TestFallbackChatterRetriesOnlyRetryableErrors(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		wantCalls int
	}{
		{"rate limit", errors.New("HTTP 429: too many requests"), 2},
		{"server error", errors.New("provider returned 503: service unavailable"), 2},
		{"timeout prose", errors.New("upstream is 5xx right now"), 2},
		{"dial timeout", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}, 2},
		{"unauthorized", errors.New("provider returned 401 Unauthorized: invalid key"), 1},
		{"validation", errors.New("400 invalid request: unknown role"), 1},
		{"content filter", errors.New("provider returned 400 Bad Request: content_filter"), 1},
		{"hard quota 402", errors.New("provider returned 402 Payment Required: quota exceeded"), 1},
		{"hard quota 429", errors.New("provider returned 429: quota_exceeded for this account"), 1},
		{"forbidden", errors.New("403 forbidden: permission denied"), 1},
		{"unknown application error", errors.New("record not found"), 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inner := &fallbackScriptChatter{script: func(_ int, model string, onText func(string)) (llm.Message, error) {
				if model == "backup" {
					onText("backup answer")
					return llm.Message{Role: "assistant", Content: "backup answer"}, nil
				}
				return llm.Message{}, tt.err
			}}
			chatter := &fallbackChatter{primary: inner, fallback: "backup"}

			var shown strings.Builder
			reply, err := chatter.Chat(context.Background(), "primary", nil, nil, func(delta string) {
				shown.WriteString(delta)
			})
			if tt.wantCalls == 1 {
				if err == nil || !errors.Is(err, tt.err) {
					t.Fatalf("err = %v, want the original error", err)
				}
				if shown.Len() != 0 {
					t.Fatalf("streamed text = %q, want empty", shown.String())
				}
			} else {
				if err != nil {
					t.Fatalf("Chat: %v", err)
				}
				if reply.Content != "backup answer" {
					t.Fatalf("reply = %q, want the backup answer", reply.Content)
				}
			}
			if len(inner.calls) != tt.wantCalls {
				t.Fatalf("requests = %d, want %d", len(inner.calls), tt.wantCalls)
			}
		})
	}
}

// TestFallbackChatterDoesNotRetryAfterCancellation keeps the cancellation
// guarantee: a cancelled context stops the run with a single request.
func TestFallbackChatterDoesNotRetryAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inner := &fallbackScriptChatter{script: func(_ int, _ string, _ func(string)) (llm.Message, error) {
		cancel()
		return llm.Message{}, errors.New("HTTP 429: too many requests")
	}}
	chatter := &fallbackChatter{primary: inner, fallback: "backup"}

	// The provider's error is returned as-is; the point is that the cancelled
	// context stops the run without issuing a second request.
	if _, err := chatter.Chat(ctx, "primary", nil, nil, nil); err == nil {
		t.Fatal("cancelled run returned nil error")
	}
	if len(inner.calls) != 1 {
		t.Fatalf("requests = %d, want 1 (cancellation must not retry)", len(inner.calls))
	}
}

// TestFallbackChatterDropsPartialFallbackTextOnFailure covers the case where
// both models fail: neither model's partial text may reach the operator.
func TestFallbackChatterDropsPartialFallbackTextOnFailure(t *testing.T) {
	inner := &fallbackScriptChatter{script: func(call int, _ string, onText func(string)) (llm.Message, error) {
		if call == 1 {
			onText("HALF-FROM-PRIMARY-")
			return llm.Message{}, errors.New("HTTP 503: service unavailable")
		}
		onText("HALF-FROM-BACKUP-")
		return llm.Message{}, errors.New("HTTP 502: bad gateway")
	}}
	chatter := &fallbackChatter{primary: inner, fallback: "backup"}

	var shown strings.Builder
	if _, err := chatter.Chat(context.Background(), "primary", nil, nil, func(delta string) {
		shown.WriteString(delta)
	}); err == nil {
		t.Fatal("both models failed but Chat returned nil error")
	}
	if got := shown.String(); got != "" {
		t.Fatalf("streamed text = %q, want empty (failed attempts must not leak)", got)
	}
	if len(inner.calls) != 2 {
		t.Fatalf("requests = %d, want 2", len(inner.calls))
	}
}
