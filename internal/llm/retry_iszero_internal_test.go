package llm

import (
	"context"
	"testing"
	"time"
)

// TestRetryPolicyIsZeroIncludesFunctionFields 守护 IsZero 上记录的约定：
// 只有当没有任何字段被设置时，策略才是"零值"。两个函数
// 字段是该约定的一部分。把只带有 OnRetry 钩子（或只有 Jitter 来源）
// 的策略当作零值，会让每个线客户端丢弃它并安装 DefaultRetryPolicy，
// 从而悄悄丢弃调用方的重试通知
// 和确定性退避。
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
