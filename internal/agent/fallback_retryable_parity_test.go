package agent

import (
	"errors"
	"testing"
)

// TestIsRetryableProviderErrorHardQuota 守护两层判定一致：硬性配额即使以
// 429/402 出现也不可重试，避免 app 判终态而 agent 重复发请求（重复计费）。
func TestIsRetryableProviderErrorHardQuota(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"insufficient quota 429", errors.New("HTTP 429: insufficient_quota: your credit balance is exhausted"), false},
		{"quota exhausted 402", errors.New("HTTP 402: quota_exhausted"), false},
		{"plain 429 rate limit", errors.New("HTTP 429: too many requests, slow down"), true},
		{"503 overloaded", errors.New("HTTP 503: overloaded"), true},
		{"context window exceeded", errors.New("context window exceeded: reduce messages"), false},
		{"invalid request", errors.New("invalid request: bad parameter"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isRetryableProviderError(tc.err)
			if got != tc.want {
				t.Fatalf("isRetryableProviderError(%q) = %v, want %v", tc.err.Error(), got, tc.want)
			}
		})
	}
}
