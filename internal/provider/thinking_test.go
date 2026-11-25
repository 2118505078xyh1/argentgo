package provider_test

import (
	"testing"

	"example.com/argentgo/internal/llm"
	"example.com/argentgo/internal/provider"
)

func TestThinkingSupportClassifiesEachProtocol(t *testing.T) {
	cases := []struct {
		protocol string
		want     string
	}{
		{string(provider.ProtocolOpenAI), provider.ThinkingEffort},
		{string(provider.ProtocolResponses), provider.ThinkingEffort},
		{string(provider.ProtocolAnthropic), provider.ThinkingBudget},
		// 短别名和规范化
		{"openai", provider.ThinkingEffort},
		{"responses", provider.ThinkingEffort},
		{"anthropic", provider.ThinkingBudget},
		{"messages", provider.ThinkingBudget},
		{"  OPENAI-CHAT-COMPLETIONS  ", provider.ThinkingEffort},
		{"OpenAI-Responses", provider.ThinkingEffort},
		{"Anthropic-Messages", provider.ThinkingBudget},
		// 未知/空回退到无
		{"", provider.ThinkingNone},
		{"  ", provider.ThinkingNone},
		{"gemini", provider.ThinkingNone},
		{"unknown-protocol", provider.ThinkingNone},
	}
	for _, tc := range cases {
		if got := provider.ThinkingSupport(tc.protocol); got != tc.want {
			t.Errorf("ThinkingSupport(%q) = %q, want %q", tc.protocol, got, tc.want)
		}
	}
}

func TestResolveThinkingNormalizesEffort(t *testing.T) {
	cases := []struct{ in, want string }{
		{"low", "low"},
		{"LOW", "low"},
		{" medium ", "medium"},
		{"Med", "medium"},
		{"middle", "medium"},
		{"high", "high"},
		{"  High  ", "high"},
		// 未知 effort 回退到空覆盖
		{"", ""},
		{"extreme", ""},
		{"  nonsense  ", ""},
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

func TestResolveThinkingAcceptsCJKEffort(t *testing.T) {
	cases := []struct{ in, want string }{
		{"低", "low"},
		{"较低", "low"},
		{"低级", "low"},
		{"中", "medium"},
		{"中等", "medium"},
		{"中级", "medium"},
		{"高", "high"},
		{"较高", "high"},
		{"高级", "high"},
		{"  高  ", "high"},
	}
	for _, tc := range cases {
		got := provider.ResolveThinking(string(provider.ProtocolResponses), tc.in, 0)
		if got.Effort != tc.want {
			t.Errorf("ResolveThinking(%q) Effort = %q, want %q", tc.in, got.Effort, tc.want)
		}
	}
}

func TestResolveThinkingClampsBudgetAtBoundaries(t *testing.T) {
	const min, max = int64(1024), int64(200_000)
	cases := []struct {
		budget int64
		want   int64
	}{
		{-5000, 0}, // non-positive means unset
		{0, 0},
		{1, min}, // below the floor
		{min - 1, min},
		{min, min}, // inclusive floor
		{min + 1, min + 1},
		{50_000, 50_000}, // in range
		{max, max},       // inclusive ceiling
		{max + 1, max},   // above the ceiling
		{1 << 40, max},
	}
	for _, tc := range cases {
		got := provider.ResolveThinking(string(provider.ProtocolAnthropic), "", tc.budget)
		if got.BudgetTokens != tc.want {
			t.Errorf("ResolveThinking(budget=%d) BudgetTokens = %d, want %d", tc.budget, got.BudgetTokens, tc.want)
		}
		if got.Effort != "" || got.Enabled != nil {
			t.Errorf("ResolveThinking(budget=%d) set unrelated fields: %+v", tc.budget, got)
		}
	}
}

func TestResolveThinkingKeepsOnlyProtocolRelevantFields(t *testing.T) {
	// effort 协议忽略预算，budget 协议忽略 effort 字符串：两者都不会发明对方不理解的旋钮。
	// effort 字符串：两者都不会发明对方不理解的旋钮。
	if got := provider.ResolveThinking(string(provider.ProtocolOpenAI), "high", 999_999); got.Effort != "high" || got.BudgetTokens != 0 {
		t.Errorf("effort protocol leaked budget: %+v", got)
	}
	if got := provider.ResolveThinking(string(provider.ProtocolAnthropic), "high", 2048); got.BudgetTokens != 2048 || got.Effort != "" {
		t.Errorf("budget protocol leaked effort: %+v", got)
	}
}

func TestResolveThinkingToggleAndUnknownProtocol(t *testing.T) {
	// toggle 协议对任何可识别的 effort 或正预算报告 "on"，对空请求报告 "off"。
	// 正预算报告 "on"，对空请求报告 "off"。
	for _, tc := range []struct {
		effort string
		budget int64
		want   bool
	}{
		{"high", 0, true},
		{"", 1024, true},
		{"nonsense", 0, false},
		{"", -1, false},
	} {
		got := provider.ResolveThinking(provider.ThinkingToggle, tc.effort, tc.budget)
		if got.Enabled == nil || *got.Enabled != tc.want {
			t.Errorf("ResolveThinking(toggle, %q, %d) Enabled = %v, want %v", tc.effort, tc.budget, got.Enabled, tc.want)
		}
		if got.Effort != "" || got.BudgetTokens != 0 {
			t.Errorf("toggle config carried unrelated fields: %+v", got)
		}
	}

	// 未知协议无论请求如何都返回零配置。
	for _, name := range []string{"", "gemini", "bogus"} {
		got := provider.ResolveThinking(name, "high", 50_000)
		if got.Effort != "" || got.BudgetTokens != 0 || got.Enabled != nil {
			t.Errorf("ResolveThinking(%q) should be zero, got %+v", name, got)
		}
	}
}

func TestThinkingDefaultsKeepWireRequestsUnchanged(t *testing.T) {
	// 在数据层强制这一点：零 Options 必须让 Thinking 为 nil，使在接线落地前没有默认请求携带 thinking 字段。
	// 使在接线落地前没有默认请求携带 thinking 字段。
	if got := (llm.Options{}); got.Thinking != nil {
		t.Fatalf("zero llm.Options has non-nil Thinking: %+v", got.Thinking)
	}
	if got := (llm.ThinkingConfig{}); got.Effort != "" || got.BudgetTokens != 0 || got.Enabled != nil {
		t.Fatalf("zero llm.ThinkingConfig is not empty: %+v", got)
	}
}
