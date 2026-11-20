package provider_test

import (
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/provider"
)

func TestThinkingSupportClassifiesEachProtocol(t *testing.T) {
	cases := []struct {
		protocol string
		want     string
	}{
		{string(provider.ProtocolOpenAI), provider.ThinkingEffort},
		{string(provider.ProtocolResponses), provider.ThinkingEffort},
		{string(provider.ProtocolAnthropic), provider.ThinkingBudget},
		// short aliases and normalisation
		{"openai", provider.ThinkingEffort},
		{"responses", provider.ThinkingEffort},
		{"anthropic", provider.ThinkingBudget},
		{"messages", provider.ThinkingBudget},
		{"  OPENAI-CHAT-COMPLETIONS  ", provider.ThinkingEffort},
		{"OpenAI-Responses", provider.ThinkingEffort},
		{"Anthropic-Messages", provider.ThinkingBudget},
		// unknown / empty fall back to none
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
		// unknown effort falls back to an empty override
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
	// An effort protocol ignores the budget and a budget protocol ignores the
	// effort string: neither invents a knob the other side does not understand.
	if got := provider.ResolveThinking(string(provider.ProtocolOpenAI), "high", 999_999); got.Effort != "high" || got.BudgetTokens != 0 {
		t.Errorf("effort protocol leaked budget: %+v", got)
	}
	if got := provider.ResolveThinking(string(provider.ProtocolAnthropic), "high", 2048); got.BudgetTokens != 2048 || got.Effort != "" {
		t.Errorf("budget protocol leaked effort: %+v", got)
	}
}

func TestResolveThinkingToggleAndUnknownProtocol(t *testing.T) {
	// A toggle protocol reports "on" for any recognized effort or a positive
	// budget, and "off" for an empty request.
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

	// Unknown protocols return the zero config regardless of the request.
	for _, name := range []string{"", "gemini", "bogus"} {
		got := provider.ResolveThinking(name, "high", 50_000)
		if got.Effort != "" || got.BudgetTokens != 0 || got.Enabled != nil {
			t.Errorf("ResolveThinking(%q) should be zero, got %+v", name, got)
		}
	}
}

func TestThinkingDefaultsKeepWireRequestsUnchanged(t *testing.T) {
	// Forcing this at the data layer: the zero Options must leave Thinking nil,
	// so no default request carries a thinking field until wiring lands.
	if got := (llm.Options{}); got.Thinking != nil {
		t.Fatalf("zero llm.Options has non-nil Thinking: %+v", got.Thinking)
	}
	if got := (llm.ThinkingConfig{}); got.Effort != "" || got.BudgetTokens != 0 || got.Enabled != nil {
		t.Fatalf("zero llm.ThinkingConfig is not empty: %+v", got)
	}
}
