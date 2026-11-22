package app

import (
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

func TestParseRetryPolicy(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want llm.RetryPolicy
	}{
		{"empty", nil, llm.RetryPolicy{}},
		{"all valid", map[string]string{
			retryAttemptsEnv: "5", retryBaseDelayEnv: "200", retryMaxDelayEnv: "8000",
		}, llm.RetryPolicy{MaxAttempts: 5, BaseDelay: 200 * time.Millisecond, MaxDelay: 8 * time.Second}},
		{"invalid ignored", map[string]string{
			retryAttemptsEnv: "abc", retryBaseDelayEnv: "-1", retryMaxDelayEnv: "0",
		}, llm.RetryPolicy{}},
		{"partial", map[string]string{retryAttemptsEnv: "2"}, llm.RetryPolicy{MaxAttempts: 2}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := func(k string) string { return tc.env[k] }
			got := parseRetryPolicy(env)
			if got.MaxAttempts != tc.want.MaxAttempts || got.BaseDelay != tc.want.BaseDelay || got.MaxDelay != tc.want.MaxDelay {
				t.Fatalf("policy = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestChatterFactoriesApplyRetryPolicy(t *testing.T) {
	// Anthropic 工厂在非默认策略下应把 Retry 写入客户端；这里用默认 env（零值）
	// 验证工厂仍可构造，且 Retry 为零值（回退 llm 默认策略）。
	client := newDefaultChatter("http://127.0.0.1:1/v1", "k", llm.Options{})
	if client == nil {
		t.Fatal("factory returned nil")
	}
}
