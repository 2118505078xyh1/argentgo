// Package tokenize holds the token-cost estimate shared by the agent's budget
// loop and the tools' output-volume hints. It sits below both packages so the
// wide-rune table has exactly one definition: it used to be copied three times
// (agent, bash volume hint, repo map) with comments asking humans to keep the
// copies in sync.
//
// The estimate is deliberately cheap and deliberately an over-estimate: it is
// used to stop a prompt from overflowing a model context window, not to price
// a request. Wide (CJK/fullwidth/emoji) runes cost roughly one token each in
// every provider tokenizer; other runes follow the chars/4 rule. Counting them
// separately avoids the severe under-estimate that chars/4 alone gives CJK,
// while still over-estimating relative to the most efficient tokenizers (the
// safe direction for a budget).
package tokenize

// CharsPerToken is the rule-of-thumb ratio for English text and code.
const CharsPerToken = 4

// Estimate returns the estimated token cost of text. Empty text costs nothing.
func Estimate(text string) int {
	if text == "" {
		return 0
	}
	// Fast path: pure-ASCII text needs neither rune decoding nor the wide-rune
	// table, and this estimate runs over whole transcripts several times per
	// turn.
	for i := 0; i < len(text); i++ {
		if text[i] >= 0x80 {
			return estimateMixed(text)
		}
	}
	return (len(text) + CharsPerToken - 1) / CharsPerToken
}

// estimateMixed is the rune-aware path for text that contains at least one
// non-ASCII byte.
func estimateMixed(text string) int {
	wide, narrow := 0, 0
	for _, r := range text {
		if IsWide(r) {
			wide++
		} else {
			narrow++
		}
	}
	return wide + (narrow+CharsPerToken-1)/CharsPerToken
}

// IsWide reports whether r belongs to a script that tokenizers treat as
// roughly one token per rune: Han, Kana, Hangul, fullwidth forms, CJK
// punctuation and common emoji.
func IsWide(r rune) bool {
	return (r >= 0x1100 && r <= 0x115F) || // Hangul Jamo
		(r >= 0x2E80 && r <= 0xA4CF) || // CJK radicals, Kangxi, Han
		(r >= 0xAC00 && r <= 0xD7A3) || // Hangul syllables
		(r >= 0xF900 && r <= 0xFAFF) || // CJK compatibility ideographs
		(r >= 0xFE10 && r <= 0xFE19) || // CJK punctuation
		(r >= 0xFE30 && r <= 0xFE6F) || // CJK compatibility forms
		(r >= 0xFF00 && r <= 0xFF60) || // fullwidth forms
		(r >= 0xFFE0 && r <= 0xFFE6) ||
		(r >= 0x1F300 && r <= 0x1FAFF) || // symbols/emoji are commonly 1+ tokens
		(r >= 0x20000 && r <= 0x3FFFD) // CJK extension planes
}
