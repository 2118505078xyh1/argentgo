package agent

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// Stop reasons reported in Result.Stopped and Event.Reason. They are stable
// strings so frontends and tests can compare them directly.
const (
	// StopComplete means the model answered without asking for more tools.
	StopComplete = "complete"
	// StopMaxTurns means the turn budget was exhausted.
	StopMaxTurns = "max_turns"
	// StopMaxPromptTokens means one request would exceed MaxPromptTokens.
	StopMaxPromptTokens = "max_prompt_tokens"
	// StopMaxTotalTokens means the run's prompt+completion total hit the cap.
	StopMaxTotalTokens = "max_total_tokens"
	// StopDeclaredTurnBudget means the per-turn cap the user declared in the
	// prompt (see ParseTurnBudget) was hit. It is opt-in via Budget field.
	StopDeclaredTurnBudget = "declared_turn_budget"
	// StopMaxCost means the estimated spend hit MaxCostUSD.
	StopMaxCost = "max_cost"
	// StopCancelled means the context was cancelled.
	StopCancelled = "cancelled"
	// StopProviderError means the provider failed; Run also returns an error.
	StopProviderError = "provider_error"
	// StopStuck is reserved for runs halted by stuck detection. Detection
	// nudges by default and does not stop, so no stop logic uses this yet.
	StopStuck = "stuck"
)

// Price is what a provider charges for one million tokens of a model.
type Price struct {
	// Input is USD per million prompt tokens.
	Input float64
	// Output is USD per million completion tokens.
	Output float64
}

// Cost estimates the spend for one usage snapshot. Cached prompt tokens are
// billed as ordinary prompt tokens; the provider's cache discount is not
// modelled.
func (p Price) Cost(u llm.Usage) float64 {
	return float64(u.PromptTokens)/1_000_000*p.Input +
		float64(u.CompletionTokens)/1_000_000*p.Output
}

// PriceFor looks a model up in a price table, falling back to the "" entry.
func PriceFor(prices map[string]Price, model string) (Price, bool) {
	if len(prices) == 0 {
		return Price{}, false
	}
	if p, ok := prices[model]; ok {
		return p, true
	}
	p, ok := prices[""]
	return p, ok
}

// Budget limits one run. A zero field is unlimited.
type Budget struct {
	// MaxTurns caps the number of model requests.
	MaxTurns int
	// MaxPromptTokens caps the prompt size of a single request, in tokens.
	// Trimming runs first; this is the hard stop when trimming cannot help.
	MaxPromptTokens int
	// MaxTotalTokens caps cumulative prompt+completion tokens.
	MaxTotalTokens int
	// MaxCostUSD caps the estimated spend, using Options.Prices.
	MaxCostUSD float64
	// DeclaredTurnTokens is the per-turn output budget the user declared in
	// their prompt (parse it with ParseTurnBudget). Zero means no declaration.
	// It is an additional cumulative token cap and never relaxes the others.
	DeclaredTurnTokens int64
}

// Breach returns the first exhausted limit as a stable reason string and a
// human-readable detail line. ok is false when the run may continue.
//
// lastPromptTokens is the prompt size of the most recent request (reported by
// the provider, or estimated); costKnown is false when no price matched.
func (b Budget) Breach(used llm.Usage, lastPromptTokens int, spentUSD float64, costKnown bool) (reason, detail string, ok bool) {
	if b.DeclaredTurnTokens > 0 {
		total := int64(used.PromptTokens) + int64(used.CompletionTokens)
		if total >= b.DeclaredTurnTokens {
			return StopDeclaredTurnBudget,
				fmt.Sprintf("%s: %d tokens used (declared limit %d)", StopDeclaredTurnBudget, total, b.DeclaredTurnTokens),
				true
		}
	}
	if b.MaxTotalTokens > 0 {
		total := used.PromptTokens + used.CompletionTokens
		if total >= b.MaxTotalTokens {
			return StopMaxTotalTokens,
				fmt.Sprintf("%s: %d tokens used (limit %d)", StopMaxTotalTokens, total, b.MaxTotalTokens),
				true
		}
	}
	if b.MaxCostUSD > 0 && costKnown && spentUSD >= b.MaxCostUSD {
		return StopMaxCost,
			fmt.Sprintf("%s: $%.4f spent (limit $%.4f)", StopMaxCost, spentUSD, b.MaxCostUSD),
			true
	}
	if b.MaxPromptTokens > 0 && lastPromptTokens >= b.MaxPromptTokens {
		return StopMaxPromptTokens,
			fmt.Sprintf("%s: prompt %d tokens (limit %d)", StopMaxPromptTokens, lastPromptTokens, b.MaxPromptTokens),
			true
	}
	return "", "", false
}

// Context-aware compaction thresholds. A percentage of the model context
// window is the preferred trigger; the token constants are fallbacks for
// configurations that do not publish a context window.
const (
	defaultCompactionTokens = 32000
	// reservedOutputFraction withholds 1/20 of the window for the reply that
	// follows a summarised prompt; reservedOutputTokens caps that reserve.
	reservedOutputFraction = 20
	reservedOutputTokens   = 8000
	// Estimate inflation is 1.3x expressed as integers: ceil(n*13/10).
	estimateInflationNumerator   = 13
	estimateInflationDenominator = 10
)

// CompactionThreshold returns the estimated-token trigger for auto-compaction.
//
// A positive percent wins: the trigger is floor(contextWindow*percent/100)
// minus a reserved output buffer of min(contextWindow/20, 8000), clamped to at
// least 1. Otherwise a positive configuredTokens is used verbatim, and the
// compiled default is the last resort. Non-positive percent values (-1/0)
// inherit the global threshold, matching the per-profile config semantics.
func CompactionThreshold(contextWindow, configuredTokens, percent int) int {
	if percent > 0 {
		reserve := contextWindow / reservedOutputFraction
		if reserve > reservedOutputTokens {
			reserve = reservedOutputTokens
		}
		// int64 product avoids overflow on large windows before the /100.
		threshold := int(int64(contextWindow)*int64(percent)/100) - reserve
		if threshold < 1 {
			return 1
		}
		return threshold
	}
	if configuredTokens > 0 {
		return configuredTokens
	}
	return defaultCompactionTokens
}

// InflateEstimate applies the 1.3x safety factor used before comparing an
// estimated prompt size against a compaction threshold. Integer math only,
// rounded up (ceil), so n=1 becomes 2 and n=100 becomes 130.
func InflateEstimate(n int) int {
	if n <= 0 {
		return 0
	}
	return int((int64(n)*estimateInflationNumerator + estimateInflationDenominator - 1) / estimateInflationDenominator)
}

// sliceForChild derives a child run's budget from the parent's: half of every
// limit, so a delegate cannot spend the whole parent allowance.
func (b Budget) sliceForChild() Budget {
	out := Budget{}
	if b.MaxTurns > 0 {
		out.MaxTurns = max(1, b.MaxTurns/2)
	}
	if b.MaxPromptTokens > 0 {
		out.MaxPromptTokens = max(1, b.MaxPromptTokens/2)
	}
	if b.MaxTotalTokens > 0 {
		out.MaxTotalTokens = max(1, b.MaxTotalTokens/2)
	}
	if b.MaxCostUSD > 0 {
		out.MaxCostUSD = b.MaxCostUSD / 2
	}
	if b.DeclaredTurnTokens > 0 {
		out.DeclaredTurnTokens = max(int64(1), b.DeclaredTurnTokens/2)
	}
	return out
}

// Limits applied to a user-declared turn budget. Declarations outside the
// range are clamped rather than rejected so a typo cannot disable the feature.
const (
	turnBudgetMinTokens = int64(1_000)
	turnBudgetMaxTokens = int64(100_000_000)
)

// Patterns used to remove everything that is not the user's own prose before
// a declaration is looked for. RE2 has no backreferences, so each injection
// container is matched by its own compiled pattern (built below).
var (
	fencedCodeBacktick = regexp.MustCompile("(?s)```.*?```")
	fencedCodeTilde    = regexp.MustCompile("(?s)~~~.*?~~~")
	inlineCode         = regexp.MustCompile("`[^`\\n]*`")
	genericHTMLTag     = regexp.MustCompile(`(?s)<[A-Za-z/][^>]*>`)
	atReference        = regexp.MustCompile(`(?m)@[A-Za-z0-9_./:\\-]+[^\n]*`)
	atBracketReference = regexp.MustCompile(`(?m)@<[^>\n]*>[^\n]*`)
	atQuotedReference  = regexp.MustCompile(`(?m)@"[^"\n]*"[^\n]*`)

	// plusBudget matches the "+500k" / "+2m" shorthand (k = thousand,
	// m = million). The sign is what makes a bare "500k" in prose safe to
	// ignore, while the suffix word boundary rejects things like "500miles".
	plusBudget = regexp.MustCompile(`\+\s*(\d+(?:\.\d+)?)\s*([kKmM])\b`)
	// verbBudget matches "use 2m tokens" style phrases. A verb plus the
	// "tokens" noun is required so ordinary quantities are not declarations.
	verbBudget = regexp.MustCompile(`(?i)\b(?:use|spend|budget|limit|cap|with|set|allow|allocate|reserve)\s+(\d+(?:\.\d+)?)\s*([kKmM])\s+(?:(?:output|total|context)\s+)?tokens?\b`)
)

// injectionBlockTags are wrappers the harness uses to inject content that is
// not the user's own text. The whole element (including its body) is dropped
// so a budget-looking number inside an attachment cannot set the limit.
var injectionBlockTags = []string{
	"system-reminder", "system_reminder", "project-memory", "file",
	"attachment", "attachments", "document", "context", "resource",
	"resources", "mcp", "tool_result", "tool-result", "function_results",
	"injected", "environment",
}

var injectionBlockPatterns = func() []*regexp.Regexp {
	out := make([]*regexp.Regexp, 0, len(injectionBlockTags)*2)
	for _, name := range injectionBlockTags {
		out = append(out,
			regexp.MustCompile(`(?is)<`+name+`\b[^>]*>.*?</`+name+`\s*>`),
			regexp.MustCompile(`(?is)<`+name+`\b[^>]*/>`),
		)
	}
	return out
}()

// ParseTurnBudget extracts the output budget the user declared for the current
// turn. Two spellings are recognised, case-insensitively and with optional
// decimals: "+500k" (also "+2m", "+1.5m") and "use 2m tokens" (with a small
// set of budget verbs and an optional "output"/"total"/"context" qualifier).
// The leftmost declaration in the user's text wins.
//
// Only the user's own prose is inspected. Code spans, <system-reminder> and
// other injected XML blocks, @file/MCP references and remaining HTML tags are
// stripped first, so a number inside an attachment cannot set the budget.
// The result is clamped to [1k, 100m]; ok is false when no declaration exists.
func ParseTurnBudget(prompt string) (tokens int64, ok bool) {
	text := stripNonUserText(prompt)

	// Find the leftmost of the two recognised forms.
	best := len(text) + 1
	var number, suffix string
	if loc := plusBudget.FindStringSubmatchIndex(text); loc != nil {
		best, number, suffix = loc[0], text[loc[2]:loc[3]], text[loc[4]:loc[5]]
	}
	if loc := verbBudget.FindStringSubmatchIndex(text); loc != nil && loc[0] < best {
		best, number, suffix = loc[0], text[loc[2]:loc[3]], text[loc[4]:loc[5]]
	}
	if best > len(text) {
		return 0, false
	}

	tokens = parseTokenCount(number, suffix)
	if tokens < turnBudgetMinTokens {
		tokens = turnBudgetMinTokens
	} else if tokens > turnBudgetMaxTokens {
		tokens = turnBudgetMaxTokens
	}
	return tokens, true
}

// stripNonUserText removes non-prose content from a prompt. The order matters:
// code and injected blocks go before @-references, and generic tags go last
// so the user's own text around a tag survives.
func stripNonUserText(prompt string) string {
	s := fencedCodeBacktick.ReplaceAllString(prompt, "")
	s = fencedCodeTilde.ReplaceAllString(s, "")
	s = inlineCode.ReplaceAllString(s, "")
	for _, re := range injectionBlockPatterns {
		s = re.ReplaceAllString(s, "")
	}
	s = atReference.ReplaceAllString(s, "")
	s = atBracketReference.ReplaceAllString(s, "")
	s = atQuotedReference.ReplaceAllString(s, "")
	s = genericHTMLTag.ReplaceAllString(s, "")
	return s
}

// parseTokenCount turns "1.5" + "m" into 1_500_000. Integer math avoids
// floating-point drift; oversized inputs saturate before ParseTurnBudget
// clamps them, so an absurd number cannot overflow int64.
func parseTokenCount(number, suffix string) int64 {
	multiplier := int64(1)
	switch suffix {
	case "k", "K":
		multiplier = 1_000
	case "m", "M":
		multiplier = 1_000_000
	}

	const saturation = int64(1 << 62)
	parts := strings.SplitN(number, ".", 2)
	var total int64
	for _, c := range parts[0] {
		d := int64(c - '0')
		if total > (saturation-d)/10 {
			return saturation
		}
		total = total*10 + d
	}
	if total > saturation/multiplier {
		return saturation
	}
	total *= multiplier

	if len(parts) == 2 {
		frac := parts[1]
		if len(frac) > 6 {
			frac = frac[:6]
		}
		var f int64
		for _, c := range frac {
			f = f*10 + int64(c-'0')
		}
		denom := int64(1)
		for range frac {
			denom *= 10
		}
		add := f * multiplier / denom
		if total > saturation-add {
			return saturation
		}
		total += add
	}
	return total
}
