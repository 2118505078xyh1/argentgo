package agent

import "sort"

// BudgetBlock is one attributed slice of the prompt budget. Name identifies
// the contributor ("system", "history", or the file string as supplied),
// Tokens is its CJK-aware estimated cost, and Percent is its share of the
// whole breakdown in [0, 100].
type BudgetBlock struct {
	Name    string
	Tokens  int
	Percent float64
}

// Labels used for the two non-file contributors. File blocks use the supplied
// string verbatim as their Name so a caller can identify the file (or the
// labelled repo-map entry) that owns the tokens.
const (
	BudgetBlockSystem  = "system"
	BudgetBlockHistory = "history"
)

// dominantBlockPercent is the strict share above which one block is reported
// as dominating the prompt. Exactly 50% is not dominant.
const dominantBlockPercent = 50.0

// BudgetBreakdown attributes an estimated prompt size to its contributors:
// the system prompt, the conversation history and each supplied file. Token
// costs come from EstimateTokens, so wide (CJK) runes are counted correctly.
//
// The result is sorted by descending Tokens (ties broken by ascending Name),
// which puts the largest consumer first. Percent is that block's share of the
// sum of every block's Tokens; when there is nothing to count the returned
// slice is empty. Empty system/history strings and empty file entries are
// skipped. The function is pure: it reads no state, has no dependencies and
// never changes the caller's inputs.
func BudgetBreakdown(system, history string, files []string) []BudgetBlock {
	type candidate struct {
		name string
		text string
	}
	candidates := make([]candidate, 0, 2+len(files))
	if system != "" {
		candidates = append(candidates, candidate{BudgetBlockSystem, system})
	}
	if history != "" {
		candidates = append(candidates, candidate{BudgetBlockHistory, history})
	}
	for _, file := range files {
		if file != "" {
			candidates = append(candidates, candidate{file, file})
		}
	}

	blocks := make([]BudgetBlock, 0, len(candidates))
	total := 0
	for _, c := range candidates {
		tokens := EstimateTokens(c.text)
		total += tokens
		blocks = append(blocks, BudgetBlock{Name: c.name, Tokens: tokens})
	}
	if total == 0 {
		return blocks
	}
	for i := range blocks {
		blocks[i].Percent = float64(blocks[i].Tokens) / float64(total) * 100
	}

	sort.Slice(blocks, func(i, j int) bool {
		if blocks[i].Tokens != blocks[j].Tokens {
			return blocks[i].Tokens > blocks[j].Tokens
		}
		return blocks[i].Name < blocks[j].Name
	})
	return blocks
}

// DominantBlock returns the largest block of a breakdown when it owns more
// than half of the estimated tokens, and reports false otherwise. Blocks are
// expected to be sorted by BudgetBreakdown, but the maximum is found by
// scanning so the helper is correct for any input order. An empty breakdown or
// a largest block at exactly 50% yields false. This is the pure basis for an
// optional "largest: <name>" hint; no existing reminder text is modified.
func DominantBlock(blocks []BudgetBlock) (BudgetBlock, bool) {
	var best BudgetBlock
	found := false
	for _, block := range blocks {
		if !found || block.Tokens > best.Tokens {
			best, found = block, true
		}
	}
	if !found {
		return BudgetBlock{}, false
	}
	percent := best.Percent
	if percent == 0 && best.Tokens > 0 {
		// Tolerate a caller that supplied tokens but no percentages.
		total := 0
		for _, block := range blocks {
			total += block.Tokens
		}
		if total > 0 {
			percent = float64(best.Tokens) / float64(total) * 100
		}
	}
	return best, percent > dominantBlockPercent
}
