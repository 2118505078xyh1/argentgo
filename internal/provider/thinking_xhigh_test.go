package provider_test

import (
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/provider"
)

// TestResolveThinkingAcceptsXHigh 验证 xhigh 档位及其 CJK 别名被规范化为线上级别 "xhigh"。
func TestResolveThinkingAcceptsXHigh(t *testing.T) {
	cases := []struct{ in, want string }{
		{"xhigh", "xhigh"},
		{" XHIGH ", "xhigh"},
		{"超高", "xhigh"},
		{"极高", "xhigh"},
		{"极强", "xhigh"},
	}
	for _, tc := range cases {
		got := provider.ResolveThinking(provider.ThinkingEffort, tc.in, 0)
		if got.Effort != tc.want {
			t.Errorf("ResolveThinking(effort, %q) Effort = %q, want %q", tc.in, got.Effort, tc.want)
		}
		if got.BudgetTokens != 0 || got.Enabled != nil {
			t.Errorf("ResolveThinking(effort, %q) set unrelated fields: %+v", tc.in, got)
		}
	}
}

// TestDefaultThinkingBudgetByTier 验证各档位的默认预算，以及未知/空档位返回 0（未设置）。
func TestDefaultThinkingBudgetByTier(t *testing.T) {
	cases := []struct {
		effort string
		want   int64
	}{
		{"low", 1024},
		{"较低", 1024},
		{"medium", 2048},
		{"中", 2048},
		{"high", 4096},
		{"高", 4096},
		{"xhigh", 6144},
		{"超高", 6144},
		{"", 0},
		{"nonsense", 0},
	}
	for _, tc := range cases {
		if got := provider.DefaultThinkingBudget(tc.effort); got != tc.want {
			t.Errorf("DefaultThinkingBudget(%q) = %d, want %d", tc.effort, got, tc.want)
		}
	}
}
