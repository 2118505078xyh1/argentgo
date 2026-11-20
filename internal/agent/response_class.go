package agent

import (
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// ResponseKind is the completeness class of a single model reply. It is derived
// from what the reply actually carries (tool parts, visible text, reasoning)
// plus the provider's finish reason — never from Result.Stopped, which the loop
// writes only after the run has ended.
//
// The four primary kinds mirror the OpenHands/kilocode response audit:
//
//	ResponseToolCalls      one or more tool parts are present;
//	ResponseContent        a visible answer arrived, with no tool parts;
//	ResponseReasoningOnly  only private reasoning (and/or usage) arrived;
//	ResponseEmpty          nothing usable arrived at all.
//
// ResponseToolCallsMissing is a defensive classification, not a fifth kind of
// answer: the provider announced a tool turn but no tool parts came back. Such
// a reply must be downgraded to a normal stop, never dispatched as a tool turn
// or used to trigger another nudge — otherwise the loop re-prompts with an
// unchanged transcript and can spin until the turn budget expires.
//
// The numeric values start at 1 so the zero value is not a valid kind; callers
// that forget to classify cannot accidentally look like a healthy answer.
type ResponseKind uint8

const (
	ResponseToolCalls ResponseKind = iota + 1
	ResponseContent
	ResponseReasoningOnly
	ResponseEmpty
	ResponseToolCallsMissing
)

// isToolCallsFinish reports whether a provider finish reason announces a tool
// turn. It covers the spellings of the supported protocols: OpenAI's
// "tool_calls", Anthropic's "tool_use", and the Responses API's
// "function_call" / "function_calls". Matching is case- and space-insensitive,
// mirroring isLengthReason in dispatch.go.
func isToolCallsFinish(reason string) bool {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "tool_calls", "tool_use", "function_call", "function_calls":
		return true
	default:
		return false
	}
}

// ClassifyResponse is a pure function: it reads only its arguments, performs no
// I/O and mutates no state. Usage/token metadata is deliberately absent from
// its inputs, so a reply that carries only reasoning or only a usage record
// cannot be mistaken for a visible answer.
//
// Precedence is intentional:
//
//  1. Real tool parts win over any finish reason — a truncated tool batch is
//     still a tool batch (the loop handles length separately).
//  2. A finish reason that promises tools but delivered none is the downgrade
//     signal; it outranks content/reasoning so callers stop instead of nudging
//     a malformed tool turn back into another identical request.
//  3. Visible content outranks reasoning: a short text answer alongside a
//     long thought is a CONTENT reply, not a reasoning-only one.
func ClassifyResponse(hasToolCalls, hasVisibleContent, hasReasoning bool, finishReason string) ResponseKind {
	if hasToolCalls {
		return ResponseToolCalls
	}
	if isToolCallsFinish(finishReason) {
		return ResponseToolCallsMissing
	}
	if hasVisibleContent {
		return ResponseContent
	}
	if hasReasoning {
		return ResponseReasoningOnly
	}
	return ResponseEmpty
}

// ClassifyReply derives the three content flags from a reply and delegates to
// ClassifyResponse. It is the single place that decides what "visible content"
// means: reasoning and usage are transcript metadata, not an answer. Whitespace
// (including Unicode spaces such as the full-width space) does not count, so a
// reply made only of blank text is EMPTY and can be corrected by the caller.
func ClassifyReply(reply llm.Message) ResponseKind {
	return ClassifyResponse(
		len(reply.ToolCalls) > 0,
		strings.TrimSpace(reply.Content) != "",
		strings.TrimSpace(reply.Reasoning) != "",
		reply.FinishReason,
	)
}

// NeedsCorrection reports whether a reply is a tool-free, content-free turn
// that a caller may nudge back into producing a usable answer. Reasoning-only
// and empty replies qualify. A malformed tool turn deliberately does not: it
// must be downgraded to a stop so an unchanged transcript is not re-sent.
func (k ResponseKind) NeedsCorrection() bool {
	return k == ResponseReasoningOnly || k == ResponseEmpty
}

// IsToolTurn reports whether the reply should be dispatched as a tool batch.
// ResponseToolCallsMissing is false here — that is the safe downgrade to a stop.
func (k ResponseKind) IsToolTurn() bool {
	return k == ResponseToolCalls
}

// IsDowngraded reports whether the finish reason claimed a tool turn that did
// not materialise. Callers should treat such a reply as a safe stop.
func (k ResponseKind) IsDowngraded() bool {
	return k == ResponseToolCallsMissing
}

// String gives stable, English names for logs and diagnostics.
func (k ResponseKind) String() string {
	switch k {
	case ResponseToolCalls:
		return "tool_calls"
	case ResponseContent:
		return "content"
	case ResponseReasoningOnly:
		return "reasoning_only"
	case ResponseEmpty:
		return "empty"
	case ResponseToolCallsMissing:
		return "tool_calls_missing"
	default:
		return "unknown"
	}
}
