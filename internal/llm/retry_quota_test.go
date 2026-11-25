package llm_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"example.com/argentgo/internal/llm"
)

func quotaNoWait(ctx context.Context, _ time.Duration) error { return ctx.Err() }

// TestQuotaClassifierSeparatesTransientWordingFromHardLimits 固定收紧后的
// nonRetryableQuotaRE：裸的 配额/额度 和裸的 "billing" 是限流措辞，
// 而非账户耗尽的证据。显式错误码仍然具有权威性。
func TestQuotaClassifierSeparatesTransientWordingFromHardLimits(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{
			name:   "zh transient rate limit with 配额 reset",
			status: 429,
			body:   `{"error":{"message":"请求过于频繁，配额将在 30 秒后重置，请稍后重试"}}`,
			want:   false,
		},
		{
			name:   "zh transient rate limit with 额度 reset",
			status: 429,
			body:   `{"error":{"message":"请求太频繁，额度将在 10 秒后重置"}}`,
			want:   false,
		},
		{
			name:   "zh subscription quota reset is still transient",
			status: 429,
			body:   `订阅额度将在 60 秒后重置，请稍后重试`,
			want:   false,
		},
		{
			name:   "zh hard 配额 exhausted",
			status: 429,
			body:   `{"error":{"message":"配额已用尽"}}`,
			want:   true,
		},
		{
			name:   "zh hard 额度 insufficient",
			status: 429,
			body:   `额度不足，请充值`,
			want:   true,
		},
		{
			name:   "zh hard subscription quota exhausted",
			status: 429,
			body:   `订阅额度已用尽`,
			want:   true,
		},
		{
			name:   "explicit quota code wins over transient zh wording",
			status: 429,
			body:   `{"error":{"code":"quota_exceeded","message":"配额将在 30 秒后重置"}}`,
			want:   true,
		},
		{
			name:   "en bare billing settings is not a hard limit",
			status: 429,
			body:   `{"error":{"message":"too many requests; check your billing settings"}}`,
			want:   false,
		},
		{
			name:   "en rate limit exceeded before billing is not a hard limit",
			status: 429,
			body:   `{"error":{"message":"rate limit exceeded, please review billing"}}`,
			want:   false,
		},
		{
			name:   "en billing with hard limit is a hard limit",
			status: 429,
			body:   `{"error":{"type":"billing","message":"hard limit reached"}}`,
			want:   true,
		},
		{
			name:   "en billing with usage limit is a hard limit",
			status: 429,
			body:   `billing usage limit exceeded`,
			want:   true,
		},
		{
			name:   "en insufficient_quota code",
			status: 429,
			body:   `{"error":{"code":"insufficient_quota"}}`,
			want:   true,
		},
		{
			name:   "quota wording on a non-quota status",
			status: 503,
			body:   `配额已用尽`,
			want:   false,
		},
		{
			name:   "credential-shaped billing value is not a limit",
			status: 429,
			body:   `api_key=billing_secret_value1234567890`,
			want:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := llm.IsNonRetryableQuotaResponse(tc.status, []byte(tc.body))
			if got != tc.want {
				t.Fatalf("IsNonRetryableQuotaResponse(%d, %q) = %v, want %v", tc.status, tc.body, got, tc.want)
			}
		})
	}
}

// TestRetryPolicyRetriesTransientChineseQuotaWording 复现该回归：
// 带 配额 重置措辞的 429 必须被重试，并遵守提供方的
// Retry-After，而不是一次尝试后就交回。
func TestRetryPolicyRetriesTransientChineseQuotaWording(t *testing.T) {
	body := `{"error":{"message":"请求过于频繁，配额将在 30 秒后重置，请稍后重试"}}`
	var sends int
	var waits []time.Duration
	policy := llm.RetryPolicy{
		MaxAttempts: 2,
		BaseDelay:   800 * time.Millisecond,
		MaxDelay:    800 * time.Millisecond,
		Jitter:      func() float64 { return 1 },
		Sleep: func(ctx context.Context, d time.Duration) error {
			waits = append(waits, d)
			return ctx.Err()
		},
	}
	resp, err := policy.Do(context.Background(), func(context.Context) (*http.Response, error) {
		sends++
		if sends == 1 {
			return &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Status:     "429 Too Many Requests",
				Header:     http.Header{"Retry-After": []string{"1"}},
				Body:       io.NopCloser(strings.NewReader(body)),
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader("ok")),
		}, nil
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if resp == nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("resp = %#v, want the successful retry", resp)
	}
	defer resp.Body.Close()
	if sends != 2 {
		t.Fatalf("sends = %d, want a retry after the transient 429", sends)
	}
	if len(waits) != 1 || waits[0] != time.Second {
		t.Fatalf("waits = %v, want one unjittered 1s Retry-After wait", waits)
	}
}

// TestRetryPolicyStopsOnHardChineseQuotaWording 保持行为的另一半：
// 已耗尽的 配额 仍然不可重试，且其响应体保留。
func TestRetryPolicyStopsOnHardChineseQuotaWording(t *testing.T) {
	body := `{"error":{"message":"配额已用尽，请更换模型"}}`
	var sends int
	policy := llm.RetryPolicy{MaxAttempts: 3, Sleep: quotaNoWait}
	resp, err := policy.Do(context.Background(), func(context.Context) (*http.Response, error) {
		sends++
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Status:     "429 Too Many Requests",
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if sends != 1 {
		t.Fatalf("sends = %d, want 1 for a hard quota", sends)
	}
	if resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("resp = %#v", resp)
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(got) != body {
		t.Fatalf("body = %q, want the original body", got)
	}
}

// TestRetryAfterIsNeverJitteredDownwards 覆盖 Retry-After
// 落在指数退避之内的窗口：服务器要求一个最小值，因此即使
// 一个略低于退避的值也不得被抖动降低（jitter=1 是
// 最强的向下拉力）。Retry-After=0 仍走抖动路径。
func TestRetryAfterIsNeverJitteredDownwards(t *testing.T) {
	cases := []struct {
		name       string
		base       time.Duration
		max        time.Duration
		attempt    int
		retryAfter time.Duration
		want       time.Duration
	}{
		{"well below backoff", 800 * time.Millisecond, 800 * time.Millisecond, 1, 700 * time.Millisecond, 800 * time.Millisecond},
		{"just below backoff", 800 * time.Millisecond, 800 * time.Millisecond, 1, 799 * time.Millisecond, 800 * time.Millisecond},
		{"exactly backoff", 800 * time.Millisecond, 800 * time.Millisecond, 1, 800 * time.Millisecond, 800 * time.Millisecond},
		{"just above backoff", 800 * time.Millisecond, 800 * time.Millisecond, 1, 801 * time.Millisecond, 801 * time.Millisecond},
		{"tiny positive value", 800 * time.Millisecond, 800 * time.Millisecond, 1, time.Nanosecond, 800 * time.Millisecond},
		{"second attempt backoff", 400 * time.Millisecond, 800 * time.Millisecond, 2, 700 * time.Millisecond, 800 * time.Millisecond},
		{"ceiling still applies", 100 * time.Millisecond, 500 * time.Millisecond, 1, 10 * time.Second, 1500 * time.Millisecond},
		{"no retry after is jittered", 800 * time.Millisecond, 800 * time.Millisecond, 1, 0, 600 * time.Millisecond},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			policy := llm.RetryPolicy{
				BaseDelay: tc.base,
				MaxDelay:  tc.max,
				Jitter:    func() float64 { return 1 },
			}
			if got := policy.Delay(tc.attempt, tc.retryAfter); got != tc.want {
				t.Fatalf("Delay(%d, %v) = %v, want %v", tc.attempt, tc.retryAfter, got, tc.want)
			}
		})
	}
}

// TestRetryAfterDoesNotConsumeJitter 明确了权威性：存在
// Retry-After 时根本不查询抖动来源。
func TestRetryAfterDoesNotConsumeJitter(t *testing.T) {
	calls := 0
	policy := llm.RetryPolicy{
		BaseDelay: 800 * time.Millisecond,
		MaxDelay:  800 * time.Millisecond,
		Jitter: func() float64 {
			calls++
			return 1
		},
	}
	if got := policy.Delay(1, 700*time.Millisecond); got != 800*time.Millisecond {
		t.Fatalf("Delay with Retry-After = %v", got)
	}
	if calls != 0 {
		t.Fatalf("jitter called %d times for an explicit Retry-After, want 0", calls)
	}
	if got := policy.Delay(1, 0); got != 600*time.Millisecond {
		t.Fatalf("Delay without Retry-After = %v, want 600ms", got)
	}
	if calls != 1 {
		t.Fatalf("jitter called %d times, want 1", calls)
	}
}
