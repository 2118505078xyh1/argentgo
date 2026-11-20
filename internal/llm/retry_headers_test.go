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

func retryHeaderNoWait(ctx context.Context, _ time.Duration) error { return ctx.Err() }

// TestRetryHeadersShouldRetryOverridesStatus 检查重试循环可观察到的决策：
// 提供方的判定胜过 HTTP 状态，而无法识别的值
// 仍像之前一样服从状态。
func TestRetryHeadersShouldRetryOverridesStatus(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		verdict   string
		wantSends int
	}{
		{"false is terminal on a retryable 503", 503, "false", 1},
		{"true retries a non-retryable 400", 400, "true", 2},
		{"unknown defers to retryable status", 503, "maybe", 2},
		{"unknown defers to non-retryable status", 400, "maybe", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sends := 0
			policy := llm.RetryPolicy{MaxAttempts: 3, Sleep: retryHeaderNoWait}
			resp, err := policy.Do(context.Background(), func(context.Context) (*http.Response, error) {
				sends++
				if sends == 1 {
					return &http.Response{
						StatusCode: tc.status,
						Status:     "provider response",
						Header:     http.Header{"X-Should-Retry": []string{tc.verdict}},
						Body:       io.NopCloser(strings.NewReader("original body")),
					}, nil
				}
				return &http.Response{StatusCode: 200, Status: "OK", Body: io.NopCloser(http.NoBody)}, nil
			})
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			if sends != tc.wantSends {
				t.Fatalf("sends = %d, want %d", sends, tc.wantSends)
			}
			if resp == nil {
				t.Fatal("Do returned a nil response")
			}
			defer resp.Body.Close()
			// 当判定停止重试时，提供方的响应体必须保持
			// 可读。在重试路径上，最后一个响应是（空的）200。
			if tc.wantSends == 1 {
				body, err := io.ReadAll(resp.Body)
				if err != nil {
					t.Fatalf("read body: %v", err)
				}
				if string(body) != "original body" {
					t.Fatalf("body = %q, want the provider's original body", body)
				}
			}
		})
	}
}

// TestRetryHeadersDelayPriority 检查哪个头部实际决定等待时长：
// 以毫秒计的 retry-after-ms 必须胜过以秒计的 Retry-After。
func TestRetryHeadersDelayPriority(t *testing.T) {
	cases := []struct {
		name    string
		headers http.Header
		want    time.Duration
	}{
		{
			name:    "retry-after-ms wins over retry-after",
			headers: http.Header{"Retry-After-Ms": []string{"700"}, "Retry-After": []string{"12"}},
			want:    700 * time.Millisecond,
		},
		{
			name:    "retry-after seconds used when ms absent",
			headers: http.Header{"Retry-After": []string{"2"}},
			want:    2 * time.Second,
		},
		{
			name:    "malformed ms falls back to retry-after",
			headers: http.Header{"Retry-After-Ms": []string{"nope"}, "Retry-After": []string{"3"}},
			want:    3 * time.Second,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var delays []time.Duration
			policy := llm.RetryPolicy{
				MaxAttempts: 2,
				Sleep: func(ctx context.Context, d time.Duration) error {
					delays = append(delays, d)
					return ctx.Err()
				},
			}
			sends := 0
			_, err := policy.Do(context.Background(), func(context.Context) (*http.Response, error) {
				sends++
				if sends == 1 {
					return &http.Response{
						StatusCode: 429,
						Status:     "429 Too Many Requests",
						Header:     tc.headers,
						Body:       io.NopCloser(strings.NewReader("rate limited")),
					}, nil
				}
				return &http.Response{StatusCode: 200, Status: "OK", Body: io.NopCloser(http.NoBody)}, nil
			})
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			if len(delays) != 1 || delays[0] != tc.want {
				t.Fatalf("sleeps = %v, want one sleep of %v", delays, tc.want)
			}
		})
	}
}

// TestRetryPolicyDeclinesOverCapProviderDelay 检查荒谬的提供方
// 提示不会被悄悄缩短成过早的重试：原始响应
// （状态和完整响应体）原样返回，且不尝试重试。
func TestRetryPolicyDeclinesOverCapProviderDelay(t *testing.T) {
	cases := []struct {
		name    string
		headers http.Header
	}{
		{"retry-after-ms over cap", http.Header{"Retry-After-Ms": []string{"999999999"}}},
		{"retry-after over cap", http.Header{"Retry-After": []string{"3600"}}},
		// 超过 time.Duration 的秒值过去会回绕为负，
		// 绕过此拒绝检查并触发重试。它必须饱和。
		{"retry-after seconds overflow", http.Header{"Retry-After": []string{"9223372037"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sends, sleeps := 0, 0
			policy := llm.RetryPolicy{
				MaxAttempts: 3,
				MaxDelay:    500 * time.Millisecond, // 上限 1.5 秒
				Sleep: func(ctx context.Context, _ time.Duration) error {
					sleeps++
					return ctx.Err()
				},
			}
			resp, err := policy.Do(context.Background(), func(context.Context) (*http.Response, error) {
				sends++
				return &http.Response{
					StatusCode: 429,
					Status:     "429 Too Many Requests",
					Header:     tc.headers,
					Body:       io.NopCloser(strings.NewReader("rate limited")),
				}, nil
			})
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			if sends != 1 || sleeps != 0 {
				t.Fatalf("sends = %d, sleeps = %d, want a single send and no retry", sends, sleeps)
			}
			if resp == nil || resp.StatusCode != 429 {
				t.Fatalf("resp = %#v, want the original 429", resp)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			if string(body) != "rate limited" {
				t.Fatalf("body = %q, want the original body", body)
			}
		})
	}
}
