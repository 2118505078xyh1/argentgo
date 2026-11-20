package llm_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// terminalNoWait keeps Do's backoff out of the tests without depending on the
// noWait helpers declared in other test files.
func terminalNoWait(ctx context.Context, _ time.Duration) error { return ctx.Err() }

func TestIsTerminalProviderOutcome(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		status int
		want   bool
	}{
		// Every terminal code is detected, on a retryable status where the
		// retry loop would otherwise wait and try again.
		{"session_budget_exceeded", `{"error":{"code":"session_budget_exceeded"}}`, 429, true},
		{"spend_limit_exceeded", `{"error":{"code":"spend_limit_exceeded"}}`, 429, true},
		{"spend_limit", `{"error":{"code":"spend_limit"}}`, 500, true},
		{"usage_accounting_failed", `{"error":{"code":"usage_accounting_failed"}}`, 503, true},
		{"inference_hook_denied", `{"error":{"code":"inference_hook_denied"}}`, 429, true},
		{"billing_hard_limit_reached", `{"error":{"code":"billing_hard_limit_reached"}}`, 429, true},
		{"insufficient_quota", `{"error":{"code":"insufficient_quota"}}`, 429, true},
		{"organization_budget_exceeded", `{"error":{"code":"organization_budget_exceeded"}}`, 429, true},

		// Case variations and separator/message forms.
		{"uppercase code", `SESSION_BUDGET_EXCEEDED`, 429, true},
		{"mixed case code", `Spend_Limit_Exceeded`, 429, true},
		{"hyphenated message", `Inference-Hook-Denied by policy`, 400, true},
		{"spaced message", `usage accounting failed`, 500, true},

		// 402 is terminal on its own, even with an unrecognised body.
		{"payment required 402", `{"error":{"message":"payment required"}}`, 402, true},
		{"payment required 402 empty body", ``, 402, true},

		// Non-terminal, transient errors must not be flagged.
		{"rate limit", `{"error":{"code":"rate_limit_exceeded","message":"try again"}}`, 429, false},
		{"too many requests", `too many requests`, 429, false},
		{"server error", `{"error":{"message":"internal server error"}}`, 500, false},
		{"timeout", `{"error":{"code":"timeout"}}`, 504, false},
		{"empty body on 429", ``, 429, false},

		// A secret that contains a pattern word is redacted before matching,
		// so it cannot create a false positive.
		{"credential-shaped value is redacted", `api_key=spend_limit_secretvalue1234567890`, 429, false},

		// Truncated/malformed bodies must not panic.
		{"truncated but still contains a code", `{"error":{"code":"session_budget_exceeded"`, 429, true},
		{"malformed without a full code", `{"error":{"code":"infer`, 429, false},
		{"random bytes", string([]byte{0x00, 0xff, 0x80, '{', '}'}), 500, false},

		// Scanning is bounded at 64KB: a code inside the window is found, a
		// code pushed beyond it is not.
		{"code within scan window then huge padding", `session_budget_exceeded` + strings.Repeat("x", 200*1024), 429, true},
		{"code beyond 64KB window", strings.Repeat("x", 70*1024) + `session_budget_exceeded`, 429, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := llm.IsTerminalProviderOutcome([]byte(tc.body), tc.status); got != tc.want {
				t.Fatalf("IsTerminalProviderOutcome(%d, %q...) = %v, want %v", tc.status, truncate(tc.body), got, tc.want)
			}
		})
	}
}

func TestTerminalOutcomeReason(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		status int
		want   string
	}{
		{"session budget", `{"error":{"code":"session_budget_exceeded"}}`, 429, "session_budget_exceeded"},
		{"organization budget", `{"error":{"code":"organization_budget_exceeded"}}`, 429, "organization_budget_exceeded"},
		{"spend limit exceeded is specific", `{"error":{"code":"spend_limit_exceeded"}}`, 429, "spend_limit_exceeded"},
		{"spend limit is generic", `{"error":{"code":"spend_limit"}}`, 429, "spend_limit"},
		{"usage accounting", `{"error":{"code":"usage_accounting_failed"}}`, 500, "usage_accounting_failed"},
		{"inference hook", `{"error":{"code":"inference_hook_denied"}}`, 429, "inference_hook_denied"},
		{"billing hard limit", `{"error":{"code":"billing_hard_limit_reached"}}`, 429, "billing_hard_limit_reached"},
		{"insufficient quota", `{"error":{"code":"insufficient_quota"}}`, 429, "insufficient_quota"},
		{"case variation keeps canonical reason", `INFERENCE_HOOK_DENIED`, 429, "inference_hook_denied"},
		{"402 payment required", ``, 402, "payment_required"},
		{"body reason wins over 402", `{"error":{"code":"insufficient_quota"}}`, 402, "insufficient_quota"},
		{"rate limit has no reason", `rate_limit_exceeded`, 429, ""},
		{"server error has no reason", `internal error`, 500, ""},
		{"empty body no reason", ``, 429, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := llm.TerminalOutcomeReason([]byte(tc.body), tc.status); got != tc.want {
				t.Fatalf("TerminalOutcomeReason(%d, %q) = %q, want %q", tc.status, tc.body, got, tc.want)
			}
		})
	}
}

// TestRetryPolicyStopsOnTerminalProviderOutcome is the integration point: a
// terminal body on a normally-retryable status must stop after one attempt and
// leave the full body readable by the caller.
func TestRetryPolicyStopsOnTerminalProviderOutcome(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		status int
	}{
		{"session budget", `{"error":{"code":"session_budget_exceeded"}}`, 429},
		{"spend limit", `{"error":{"code":"spend_limit"}}`, 500},
		{"usage accounting failed", `{"error":{"code":"usage_accounting_failed"}}`, 503},
		{"inference hook denied", `{"error":{"code":"inference_hook_denied"}}`, 429},
		{"organization budget", `{"error":{"code":"organization_budget_exceeded"}}`, 429},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var sends int
			var attempts []llm.RetryAttempt
			policy := llm.RetryPolicy{
				MaxAttempts: 3,
				Sleep:       terminalNoWait,
				OnRetry:     func(a llm.RetryAttempt) { attempts = append(attempts, a) },
			}
			resp, err := policy.Do(context.Background(), func(context.Context) (*http.Response, error) {
				sends++
				return &http.Response{
					StatusCode: tc.status,
					Status:     http.StatusText(tc.status),
					Body:       io.NopCloser(strings.NewReader(tc.body)),
				}, nil
			})
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			if sends != 1 {
				t.Fatalf("sends = %d, want 1 for a terminal outcome", sends)
			}
			if len(attempts) != 0 {
				t.Fatalf("retry attempts = %v, want none for a terminal outcome", attempts)
			}
			if resp == nil || resp.StatusCode != tc.status {
				t.Fatalf("resp = %#v", resp)
			}
			defer resp.Body.Close()
			got, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			if string(got) != tc.body {
				t.Fatalf("body = %q, want the full original body", got)
			}
		})
	}
}

// TestRetryPolicyStillRetriesRateLimitOutcome guards the other half: adding
// terminal classification must not swallow ordinary transient rate limits.
func TestRetryPolicyStillRetriesRateLimitOutcome(t *testing.T) {
	var sends int
	policy := llm.RetryPolicy{MaxAttempts: 2, Sleep: terminalNoWait}
	resp, err := policy.Do(context.Background(), func(context.Context) (*http.Response, error) {
		sends++
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Status:     "429 Too Many Requests",
			Body:       io.NopCloser(strings.NewReader("too many requests, slow down")),
		}, nil
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if sends != 2 {
		t.Fatalf("sends = %d, want 2 for a plain rate limit", sends)
	}
	if resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("resp = %#v", resp)
	}
	defer resp.Body.Close()
}

func truncate(s string) string {
	if len(s) > 48 {
		return s[:48] + "..."
	}
	return s
}
