package agent

import (
	"fmt"

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
	// StopMaxCost means the estimated spend hit MaxCostUSD.
	StopMaxCost = "max_cost"
	// StopCancelled means the context was cancelled.
	StopCancelled = "cancelled"
	// StopProviderError means the provider failed; Run also returns an error.
	StopProviderError = "provider_error"
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
}

// Breach returns the first exhausted limit as a stable reason string and a
// human-readable detail line. ok is false when the run may continue.
//
// lastPromptTokens is the prompt size of the most recent request (reported by
// the provider, or estimated); costKnown is false when no price matched.
func (b Budget) Breach(used llm.Usage, lastPromptTokens int, spentUSD float64, costKnown bool) (reason, detail string, ok bool) {
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
	return out
}
