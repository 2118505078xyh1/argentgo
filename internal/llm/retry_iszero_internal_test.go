package llm

import (
	"context"
	"testing"
	"time"
)

// TestRetryPolicyIsZeroIncludesFunctionFields guards the contract documented on
// IsZero: a policy is "zero" only when no field was set. The two function
// fields are part of that contract. Treating a policy that carries only an
// OnRetry hook (or only a Jitter source) as zero makes every wire client throw
// it away and install DefaultRetryPolicy, silently dropping the caller's
// retry notifications and deterministic backoff.
func TestRetryPolicyIsZeroIncludesFunctionFields(t *testing.T) {
	cases := []struct {
		name   string
		policy RetryPolicy
		want   bool
	}{
		{name: "empty", policy: RetryPolicy{}, want: true},
		{name: "max attempts", policy: RetryPolicy{MaxAttempts: 3}, want: false},
		{name: "base delay", policy: RetryPolicy{BaseDelay: 1}, want: false},
		{name: "max delay", policy: RetryPolicy{MaxDelay: 1}, want: false},
		{name: "sleep", policy: RetryPolicy{Sleep: func(ctx context.Context, d time.Duration) error { return nil }}, want: false},
		{name: "jitter", policy: RetryPolicy{Jitter: func() float64 { return 0 }}, want: false},
		{name: "on retry", policy: RetryPolicy{OnRetry: func(RetryAttempt) {}}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.policy.IsZero(); got != tc.want {
				t.Fatalf("IsZero() = %v, want %v", got, tc.want)
			}
		})
	}
}
