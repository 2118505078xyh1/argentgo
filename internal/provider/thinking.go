package provider

import (
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// Thinking support kinds returned by ThinkingSupport. They describe how a wire
// protocol expresses a reasoning control, not whether it is enabled.
const (
	ThinkingEffort = "effort" // a named level (reasoning_effort / reasoning.effort)
	ThinkingToggle = "toggle" // a plain on/off switch
	ThinkingBudget = "budget" // an explicit token budget (Anthropic extended thinking)
	ThinkingNone   = "none"   // no reasoning control, or an unknown protocol
)

// ThinkingSupport classifies a wire protocol by the shape of its reasoning
// control:
//
//   - "effort": OpenAI chat-completions and Responses use a named level
//     ("low"/"medium"/"high") in reasoning_effort / reasoning.effort.
//   - "budget": Anthropic Messages uses an explicit budget_tokens value on its
//     extended-thinking block.
//   - "toggle": a boolean on/off switch. No built-in protocol uses it today;
//     it is reserved for providers that expose only a toggle, and lets
//     ResolveThinking still normalize a request for such a protocol.
//   - "none": the protocol has no reasoning control. This is also the fallback
//     for an empty or unrecognised name.
//
// Matching ignores surrounding whitespace and case. Both the canonical wire
// names from internal/llm and the common short provider aliases are accepted.
// The four support kinds themselves are also accepted and returned unchanged,
// so the function is idempotent and callers can pass an already-classified
// kind. This is a pure function: no I/O and no global state.
func ThinkingSupport(protocol string) string {
	normalized := strings.ToLower(strings.TrimSpace(protocol))
	switch normalized {
	case ThinkingEffort, ThinkingToggle, ThinkingBudget, ThinkingNone:
		return normalized
	case string(ProtocolOpenAI), "openai", "chat", "chat-completions":
		return ThinkingEffort
	case string(ProtocolResponses), "responses":
		return ThinkingEffort
	case string(ProtocolAnthropic), "anthropic", "messages":
		return ThinkingBudget
	default:
		return ThinkingNone
	}
}

// Thinking budget bounds. Anthropic's extended thinking rejects budgets below
// 1024 tokens; the upper bound keeps an unreasonable request from being
// forwarded while sitting well above any current model's useful allowance.
const (
	minThinkingBudget int64 = 1024
	maxThinkingBudget int64 = 200_000
)

// normalizeEffort maps a user-supplied reasoning effort to the canonical
// lowercase level understood by the OpenAI-style wire fields. Surrounding
// whitespace is trimmed and matching is case-insensitive; common CJK spellings
// are accepted so "--thinking 高" behaves like "--thinking high". An
// unrecognised or empty value returns "" so no invalid level is ever forwarded.
func normalizeEffort(effort string) string {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "low", "较低", "低", "低级":
		return "low"
	case "medium", "med", "middle", "中等", "中", "中级":
		return "medium"
	case "high", "较高", "高", "高级":
		return "high"
	default:
		return ""
	}
}

// clampBudget bounds a thinking-token budget. A non-positive value means
// "unset" and stays 0 so the caller emits no budget field at all; a positive
// value is clamped into [minThinkingBudget, maxThinkingBudget].
func clampBudget(budget int64) int64 {
	if budget <= 0 {
		return 0
	}
	if budget < minThinkingBudget {
		return minThinkingBudget
	}
	if budget > maxThinkingBudget {
		return maxThinkingBudget
	}
	return budget
}

// ResolveThinking normalizes a user's thinking request for a wire protocol. It
// is a pure function: no I/O, no mutation of inputs, and no dependency on
// config or session state. The returned llm.ThinkingConfig is data for a
// future wire-client wiring step; until that step lands, leaving
// llm.Options.Thinking nil keeps the current wire bytes unchanged.
//
//   - effort protocols keep only a normalized Effort level;
//   - budget protocols keep only a clamped BudgetTokens value;
//   - toggle protocols set Enabled only when the request asks for "on";
//   - unsupported protocols return the zero ThinkingConfig.
func ResolveThinking(protocol, effort string, budget int64) llm.ThinkingConfig {
	switch ThinkingSupport(protocol) {
	case ThinkingEffort:
		return llm.ThinkingConfig{Effort: normalizeEffort(effort)}
	case ThinkingBudget:
		return llm.ThinkingConfig{BudgetTokens: clampBudget(budget)}
	case ThinkingToggle:
		on := normalizeEffort(effort) != "" || budget > 0
		return llm.ThinkingConfig{Enabled: &on}
	default:
		return llm.ThinkingConfig{}
	}
}
