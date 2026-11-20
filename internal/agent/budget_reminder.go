package agent

import "strconv"

// budgetTierPercents are the context-window occupancy milestones at which the
// model should get a model-visible reminder. A tier number is the count of
// milestones already crossed, so it grows monotonically as the window fills.
var budgetTierPercents = [4]int{50, 75, 90, 95}

// BudgetTier returns how many occupancy milestones used/window has crossed:
// 0 below 50%, then 1/2/3/4 at 50%/75%/90%/95%. The comparison is done with
// integer math (used*100 >= percent*window) so a boundary such as exactly half
// a window lands on the higher tier without floating-point drift.
//
// Non-positive inputs are meaningless as an occupancy ratio and return 0, so a
// caller can feed raw usage/provider values in without first sanitising them.
// Token counts are far below the int64 range, so the widened product cannot
// overflow in practice; the used >= window short-circuit also covers the case
// where the window is completely consumed.
func BudgetTier(used, window int) int {
	if used <= 0 || window <= 0 {
		return 0
	}
	if used >= window {
		return len(budgetTierPercents)
	}
	tier := 0
	for _, percent := range budgetTierPercents {
		if int64(used)*100 < int64(percent)*int64(window) {
			break
		}
		tier++
	}
	return tier
}

// ShouldRemind decides whether to deliver a budget reminder for curTier and
// records the decision in delivered so the same tier is never announced twice
// within one context window. It is a pure decision layer: no I/O, no clock, no
// event construction — the caller decides how (or whether) to surface a true
// result.
//
// A reminder is warranted when the occupancy tier rises (curTier > prevTier)
// or when a new window starts. A flat or falling tier is silent. Delivered keys
// are tier numbers; newWindow clears them first, which lets an identical tier
// warn once again in the fresh window. A nil delivered map is safe to pass: the
// decision is still returned but nothing is recorded.
func ShouldRemind(prevTier, curTier int, delivered map[int]bool, newWindow bool) bool {
	if newWindow {
		for tier := range delivered {
			delete(delivered, tier)
		}
	} else if curTier <= prevTier {
		return false
	}
	if delivered[curTier] {
		return false
	}
	if delivered != nil {
		delivered[curTier] = true
	}
	return true
}

// BudgetReminderText renders a model-visible budget reminder in English. It is
// deliberately locale-independent: the instruction is written into the prompt
// stream, where the model reads it, rather than shown to an operator.
//
// remaining is clamped to zero before formatting and rendered with a "k" unit
// (1000 -> "1k", 1234 -> "1.2k"); the exact number is an approximation, hence
// the "approximately" wording. window is accepted for symmetry with BudgetTier
// and future phrasing, but the message reports the absolute remaining count
// and is intentionally independent of it. The function is pure and stable:
// identical arguments always produce an identical string.
func BudgetReminderText(remaining, window int) string {
	_ = window
	return "You have approximately " + formatTokensK(remaining) +
		" tokens left in this context window; consider wrapping up or summarizing."
}

// formatTokensK renders a token count in thousands, rounding to the nearest
// tenth of a k and omitting a trailing ".0". Negative counts become "0k".
func formatTokensK(n int) string {
	if n < 0 {
		n = 0
	}
	k := n / 1000
	tenth := (n%1000 + 50) / 100 // 0..10, rounded to the nearest 100 tokens
	if tenth >= 10 {
		k++
		tenth = 0
	}
	if tenth == 0 {
		return strconv.Itoa(k) + "k"
	}
	return strconv.Itoa(k) + "." + strconv.Itoa(tenth) + "k"
}
