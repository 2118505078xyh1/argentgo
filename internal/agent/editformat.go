// Package agent — edit-format guidance.
//
// omp's differentiator is per-model edit-format tuning: the prompt teaches the
// model which edit strategy works best for its family. argentgo already ships
// hashline anchors (opt-in via the edit tool's hashline field) and a graded
// repair ladder (repair:true), but the system prompt never mentioned them.
// This file supplies a short, model-aware block that can be appended to the
// system prompt. It deliberately uses only the standard library and stays at
// the agent-prompt layer: no provider/wire details leak in here.
//
// Family tuning is expressed as a small profile (see editProfile). A profile
// declares (a) the hint line appended to the prompt and (b) an advisory
// "preferred edit tool" used by callers that build tool catalogues or UI
// hints. The preferred tool is deliberately NOT injected into the system
// prompt: that keeps existing prompt bytes stable, and no tool registration or
// execution depends on it.
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

// ModelFamily is a coarse family tag used only for prompt tuning. It is a
// string so it stays printable in logs and tests; callers must not use it to
// dispatch tools (see the note on editProfile.preferredTool).
type ModelFamily string

const (
	FamilyAnthropic ModelFamily = "anthropic"
	FamilyOpenAI    ModelFamily = "openai"
	FamilyGemini    ModelFamily = "gemini"
	FamilyDeepseek  ModelFamily = "deepseek"
	// FamilyHashlineChunked covers the smaller "hashline in small chunks"
	// siblings (Grok/MiniMax/Qwen) that share one hint line.
	FamilyHashlineChunked ModelFamily = "hashline-chunked"
	FamilyUnknown         ModelFamily = "unknown"
)

// Hint lines are kept as constants so that a family can be re-classified
// without silently changing the prompt text of an existing family. The first
// four strings are byte-for-byte the legacy strategyFor output.
const (
	hintHashlineAnchor = "Claude/Anthropic: prefer hashline edits (hashline:true). Set anchor to the exact text you first read and " +
		"anchor_hash to the first 12 hex chars of sha256(anchor bytes). Keep anchors small and unique."
	hintEditRepair = "GPT/OpenAI: prefer edit with one unique old->new pair; on whitespace drift retry with repair:true. " +
		"Batch several changes with multi_edit."
	hintSmallChunks = "Grok/Gemini/MiniMax/Qwen: prefer hashline edits in small chunks - read a short unique anchor, then edit " +
		"with hashline:true and anchor_hash (12-hex sha256). Split large rewrites."
	hintDeepseek = "DeepSeek: prefer edit with one unique old->new pair; use repair:true when whitespace drifted and multi_edit " +
		"for several changes. Keep anchors short and unique."
	hintUnknown = "Unknown model: both styles are available - edit (unique old->new, repair:true for whitespace drift) or " +
		"hashline (hashline:true + anchor_hash = 12-hex sha256 of the text you read). Read first; choose the best fit."
)

// editProfile is the lightweight per-family declaration. It is intentionally
// read-only and private: only the detection and advisory helpers below are
// exported so no caller can start depending on profile internals to dispatch
// tools.
type editProfile struct {
	family ModelFamily
	hint   string // strategy line appended to the system prompt
	// preferredTool is a real registered tool name advertised as the family's
	// first choice. Empty means "no strong preference". It is advisory
	// metadata for callers that build tool descriptions / UI hints; it never
	// changes tool registration or execution, and it never enters the prompt.
	preferredTool string
}

// profileFor maps a detected family to its tuning declaration. Unknown falls
// back to the legacy generic hint with no tool preference.
func profileFor(family ModelFamily) editProfile {
	switch family {
	case FamilyAnthropic:
		return editProfile{family, hintHashlineAnchor, "edit"}
	case FamilyOpenAI:
		return editProfile{family, hintEditRepair, "edit"}
	case FamilyGemini:
		// Gemini keeps the legacy Grok/Gemini/MiniMax/Qwen text verbatim.
		return editProfile{family, hintSmallChunks, "edit"}
	case FamilyDeepseek:
		return editProfile{family, hintDeepseek, "edit"}
	case FamilyHashlineChunked:
		return editProfile{family, hintSmallChunks, "edit"}
	default:
		return editProfile{FamilyUnknown, hintUnknown, ""}
	}
}

// DetectModelFamily resolves a resolved model name to a coarse family by
// case-insensitive substring, in priority order. It is a pure function: no I/O,
// no provider lookups. Unrecognised names return FamilyUnknown.
func DetectModelFamily(model string) ModelFamily {
	name := strings.ToLower(model)
	switch {
	case strings.Contains(name, "claude"), strings.Contains(name, "anthropic"):
		return FamilyAnthropic
	case strings.Contains(name, "gpt"), strings.Contains(name, "openai"),
		strings.Contains(name, "o1"), strings.Contains(name, "o3"):
		return FamilyOpenAI
	case strings.Contains(name, "gemini"):
		return FamilyGemini
	case strings.Contains(name, "deepseek"):
		return FamilyDeepseek
	case strings.Contains(name, "grok"), strings.Contains(name, "minimax"),
		strings.Contains(name, "qwen"):
		return FamilyHashlineChunked
	default:
		return FamilyUnknown
	}
}

// RecommendedEditTool returns the registered tool name that the model family
// tends to succeed with first ("edit" for every currently-profiled family),
// or "" when there is no strong preference. It is advisory only: callers may
// use it to rank tool descriptions in a catalogue or a UI hint, but tool
// registration and execution are unaffected.
func RecommendedEditTool(model string) string {
	return profileFor(DetectModelFamily(model)).preferredTool
}

// EditToolPreference returns a short advisory sentence suitable for tool
// "advertising" (catalogue blurbs, UI hints). It returns "" for unknown models
// and is never appended to the system prompt by this package.
func EditToolPreference(model string) string {
	tool := RecommendedEditTool(model)
	if tool == "" {
		return ""
	}
	return "Preferred edit tool for this model: " + tool + "."
}

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

// strategyFor picks the strategy line from the detected family's profile. The
// hint strings are unchanged from the previous substring switch, so existing
// prompt bytes are preserved for every previously-recognised family.
func strategyFor(model string) string {
	return profileFor(DetectModelFamily(model)).hint
}
