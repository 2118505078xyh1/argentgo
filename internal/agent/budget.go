package agent

import (
	"fmt"

	"github.com/2118505078xyh1/argent-go/internal/llm"
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
