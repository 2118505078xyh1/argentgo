// Package agent — edit-format guidance.
//
// omp's differentiator is per-model edit-format tuning: the prompt teaches the
// model which edit strategy works best for its family. argentgo already ships
// hashline anchors (opt-in via the edit tool's hashline field) and a graded
// repair ladder (repair:true), but the system prompt never mentioned them.
// This file supplies a short, model-aware block that can be appended to the
// system prompt. It deliberately uses only the standard library and stays at
// the agent-prompt layer: no provider/wire details leak in here.
package agent

import "strings"

// editFormatMarker lets AppendEditFormat detect its own handiwork so repeated
// appends (e.g. a schema retry) never duplicate the block.
const editFormatMarker = "<!-- edit-format-guidance -->"

// commonEditFormat is included for every model family. The two sentences are
// load-bearing: agents that emit fenced code blocks instead of calling tools
// waste turns, and edits without a prior read have no reliable anchor text.
const commonEditFormat = "All changes must go through tools (write/edit/multi_edit); never output code blocks as edits. " +
	"Always read before edit to get exact anchor text."

// EditFormatGuidance returns a short (<=600 byte) block of model-aware edit
// guidance suitable for appending to the system prompt.
//
// Family detection is heuristic and intentionally simple: a case-insensitive
// substring match against the resolved model name, in priority order. It is a
// prompt hint only - no behaviour depends on it.
func EditFormatGuidance(model string) string {
	return editFormatMarker + "\n" + strategyFor(model) + "\n" + commonEditFormat
}

// AppendEditFormat appends EditFormatGuidance to system with a blank-line
// separator. It is idempotent: if system already carries the guidance marker
// it is returned unchanged. The original text is only ever extended, never
// trimmed, so callers that pin their system prompt keep their bytes.
func AppendEditFormat(system, model string) string {
	if strings.Contains(system, editFormatMarker) {
		return system
	}
	if system == "" {
		return EditFormatGuidance(model)
	}
	sep := "\n\n"
	switch {
	case strings.HasSuffix(system, "\n\n"):
		sep = ""
	case strings.HasSuffix(system, "\n"):
		sep = "\n"
	}
	return system + sep + EditFormatGuidance(model)
}

// strategyFor picks the strategy line by case-insensitive substring. Order
// matters only for overlapping names; the families below do not genuinely
// overlap, but checking Claude first keeps Anthropic models deterministic.
func strategyFor(model string) string {
	name := strings.ToLower(model)
	switch {
	case strings.Contains(name, "claude"), strings.Contains(name, "anthropic"):
		return "Claude/Anthropic: prefer hashline edits (hashline:true). Set anchor to the exact text you first read and " +
			"anchor_hash to the first 12 hex chars of sha256(anchor bytes). Keep anchors small and unique."
	case strings.Contains(name, "gpt"), strings.Contains(name, "openai"),
		strings.Contains(name, "o1"), strings.Contains(name, "o3"):
		return "GPT/OpenAI: prefer edit with one unique old->new pair; on whitespace drift retry with repair:true. " +
			"Batch several changes with multi_edit."
	case strings.Contains(name, "grok"), strings.Contains(name, "gemini"),
		strings.Contains(name, "minimax"), strings.Contains(name, "qwen"):
		return "Grok/Gemini/MiniMax/Qwen: prefer hashline edits in small chunks - read a short unique anchor, then edit " +
			"with hashline:true and anchor_hash (12-hex sha256). Split large rewrites."
	default:
		return "Unknown model: both styles are available - edit (unique old->new, repair:true for whitespace drift) or " +
			"hashline (hashline:true + anchor_hash = 12-hex sha256 of the text you read). Read first; choose the best fit."
	}
}
