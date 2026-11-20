package tui

// Scored fuzzy subsequence matcher for the side-rail locators (sessions,
// workspace, tasks). stdlib only; UTF-8/CJK safe because every index the
// caller sees is a rune index into the original text.
//
// The implementation is a small dynamic program: it finds the subsequence
// assignment of pattern runes to text runes that maximises a score built from
// match bonuses, word-boundary bonuses, consecutive-run bonuses and gap
// penalties. It deliberately avoids fzf's Slab/charClass micro-optimisations;
// panels hold at most a few hundred short strings, so clarity wins here.

import (
	"sort"
	"unicode"
)

// Scoring weights. They are relative; tests compare outcomes, not absolutes.
const (
	fuzzyScoreMatch       = 16 // a pattern rune matched a text rune
	fuzzyScoreFirst       = 16 // the match landed on the very first text rune
	fuzzyScoreBoundary    = 8  // word boundary (space, separator, camel, digit run)
	fuzzyScorePathSep     = 4  // extra boundary weight for a path separator in pathMode
	fuzzyScoreConsecutive = 8  // this rune directly follows the previous matched rune
	fuzzyGapStart         = -3 // penalty for opening a gap of skipped runes
	fuzzyGapExtend        = -1 // penalty for each additional skipped rune
)

const fuzzyNegInf = -1 << 30

// FuzzyResult is one scored candidate. Callers build it from a FuzzyMatch call
// and keep Index pointing at their own item so sorting does not lose the
// association. SortFuzzyResults only looks at these four fields.
type FuzzyResult struct {
	Score     int
	Positions []int // rune indices of the matched characters, ascending
	TextLen   int   // rune count of the matched text (shorter wins a tie)
	Index     int   // caller-supplied source index; final deterministic tiebreak
}

// FuzzyMatch reports whether every rune of pattern appears in text in order,
// and if so the best score and the matched rune indices.
//
// Case handling is smart-case: when pattern contains no uppercase rune the
// comparison is case-insensitive (both sides folded with unicode.ToLower); the
// presence of any uppercase rune makes the comparison case-sensitive. An empty
// pattern matches everything with score 0 and no positions.
//
// pathMode treats '/', '\\', ':' and ',' as word separators (in addition to the
// always-separators '.', '_' and '-') and gives those separators an extra
// boundary bonus, which favours matches that start at path components.
func FuzzyMatch(pattern, text string, pathMode bool) (score int, positions []int, ok bool) {
	patOrig := []rune(pattern)
	txtOrig := []rune(text)
	P, T := len(patOrig), len(txtOrig)
	if P == 0 {
		return 0, nil, true
	}
	if P > T {
		return 0, nil, false
	}

	// Smart-case: fold both sides once when the pattern is all lowercase.
	// unicode.ToLower is 1:1 on runes, so folded indices stay valid for txtOrig.
	fold := !containsUpper(patOrig)
	pat, txt := patOrig, txtOrig
	if fold {
		pat = lowerRunes(patOrig)
		txt = lowerRunes(txtOrig)
	}

	// back[i][j] is the text index of the rune matched just before txt[j] when
	// pat[i] is matched at txt[j]; -1 means "no previous rune" (i == 0).
	back := make([][]int, P)

	// prev* holds the best score for pat[i-1] matched at each text position.
	prevScore := make([]int, T)
	prevValid := make([]bool, T)
	for j := 0; j < T; j++ {
		if pat[0] == txt[j] {
			prevScore[j] = charBonus(txtOrig, j, pathMode) + gapCost(j)
			prevValid[j] = true
		}
	}

	curScore := make([]int, T)
	curValid := make([]bool, T)
	for i := 1; i < P; i++ {
		// Running max of prevScore[k] - k*gapExtend for k <= j. This turns the
		// "best earlier match plus a gap" search into O(1) per text position.
		bestVal := make([]int, T)
		bestK := make([]int, T)
		runVal, runK := fuzzyNegInf, -1
		for j := 0; j < T; j++ {
			if prevValid[j] {
				v := prevScore[j] - j*fuzzyGapExtend
				if v > runVal {
					runVal, runK = v, j
				}
			}
			bestVal[j], bestK[j] = runVal, runK
		}

		back[i] = make([]int, T)
		for j := 0; j < T; j++ {
			curValid[j] = false
			if pat[i] != txt[j] {
				continue
			}
			base := charBonus(txtOrig, j, pathMode)
			best, bk := fuzzyNegInf, -1

			// Consecutive match: previous rune was at j-1, no gap penalty.
			if j >= 1 && prevValid[j-1] {
				best = prevScore[j-1] + base + fuzzyScoreConsecutive
				bk = j - 1
			}
			// Gapped match: previous rune at k <= j-2.
			if j >= 2 && bestVal[j-2] > fuzzyNegInf/2 {
				cand := bestVal[j-2] + base + fuzzyGapStart + (j-2)*fuzzyGapExtend
				if cand > best {
					best, bk = cand, bestK[j-2]
				}
			}
			if bk >= 0 {
				curScore[j], curValid[j], back[i][j] = best, true, bk
			}
		}
		prevScore, curScore = curScore, prevScore
		prevValid, curValid = curValid, prevValid
	}

	bestEnd, bestEndScore := -1, fuzzyNegInf
	for j := 0; j < T; j++ {
		if prevValid[j] && prevScore[j] > bestEndScore {
			bestEnd, bestEndScore = j, prevScore[j]
		}
	}
	if bestEnd < 0 {
		return 0, nil, false
	}

	positions = make([]int, P)
	k := bestEnd
	for i := P - 1; i >= 0; i-- {
		positions[i] = k
		if i > 0 {
			k = back[i][k]
		}
	}
	return bestEndScore, positions, true
}

// SortFuzzyResults orders results by descending score, then shorter text, then
// an earlier first matched position, then caller Index. sort.SliceStable keeps
// fully identical entries in their original order, so the result is
// deterministic for any input.
func SortFuzzyResults(results []FuzzyResult) {
	sort.SliceStable(results, func(i, j int) bool {
		a, b := results[i], results[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if a.TextLen != b.TextLen {
			return a.TextLen < b.TextLen
		}
		ai, bi := startOf(a.Positions), startOf(b.Positions)
		if ai != bi {
			return ai < bi
		}
		return a.Index < b.Index
	})
}

// startOf returns the first matched rune index, or a large sentinel for an
// empty position list so such results sort last on the "earlier start" rule.
func startOf(positions []int) int {
	if len(positions) == 0 {
		return 1 << 30
	}
	return positions[0]
}

// charBonus is the non-position-dependent part of matching txt[j]: a flat
// match point plus a first-rune bonus and/or a word-boundary bonus.
func charBonus(txtOrig []rune, j int, pathMode bool) int {
	bonus := fuzzyScoreMatch
	if j == 0 {
		bonus += fuzzyScoreFirst
	}
	if boundary, pathSep := isBoundary(txtOrig, j, pathMode); boundary {
		bonus += fuzzyScoreBoundary
		if pathSep {
			bonus += fuzzyScorePathSep
		}
	}
	return bonus
}

// gapCost is the penalty for skipping n runes between two matched runes (or
// before the first match). Longer gaps cost progressively more.
func gapCost(n int) int {
	if n <= 0 {
		return 0
	}
	return fuzzyGapStart + (n-1)*fuzzyGapExtend
}

// isBoundary reports whether the rune at j starts a new word: after whitespace,
// after a separator, at a camelCase uppercase, or at a digit/non-digit switch.
// The second return value is true only for a path separator in pathMode.
func isBoundary(txt []rune, j int, pathMode bool) (boundary, pathSep bool) {
	if j <= 0 || j >= len(txt) {
		return false, false
	}
	prev, cur := txt[j-1], txt[j]
	if unicode.IsSpace(prev) {
		return true, false
	}
	if fuzzySeparator(prev, pathMode) {
		return true, pathMode && isPathSeparator(prev)
	}
	if unicode.IsUpper(cur) && !unicode.IsUpper(prev) {
		return true, false
	}
	if unicode.IsDigit(cur) != unicode.IsDigit(prev) {
		return true, false
	}
	return false, false
}

func fuzzySeparator(r rune, pathMode bool) bool {
	switch r {
	case '.', '_', '-':
		return true
	case '/', '\\', ':', ',':
		return pathMode
	}
	return false
}

func isPathSeparator(r rune) bool {
	switch r {
	case '/', '\\', ':', ',':
		return true
	}
	return false
}

func containsUpper(rs []rune) bool {
	for _, r := range rs {
		if unicode.IsUpper(r) {
			return true
		}
	}
	return false
}

func lowerRunes(rs []rune) []rune {
	out := make([]rune, len(rs))
	for i, r := range rs {
		out[i] = unicode.ToLower(r)
	}
	return out
}
