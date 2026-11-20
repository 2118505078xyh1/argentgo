package tui

import (
	"unicode"
	"unicode/utf8"
)

// WordSegment is a contiguous run of text on one side of a word diff. Changed
// reports that the run has no counterpart on the other side. Concatenating the
// Text of every segment reproduces the original line exactly, whitespace and
// punctuation included.
type WordSegment struct {
	Text    string
	Changed bool
}

// maxWordDiffCells caps the token LCS table, mirroring maxDiffLCSCells in
// diff.go. A line pair this large is not worth aligning token by token.
const maxWordDiffCells = 64 * 1024

// WordDiff aligns a pair of -/+ lines at token granularity and returns the
// unique runs of each side. Small edits highlight only the words involved;
// loosely related lines degrade to one whole-line Changed segment on each
// side rather than producing a distracting stripe of coloured fragments.
// Empty input yields no segments.
func WordDiff(oldLine, newLine string) (oldSegs, newSegs []WordSegment) {
	if oldLine == newLine {
		if oldLine == "" {
			return nil, nil
		}
		return []WordSegment{{Text: oldLine}}, []WordSegment{{Text: newLine}}
	}

	oldToks, newToks := wordTokens(oldLine), wordTokens(newLine)
	n, m := len(oldToks), len(newToks)
	if n == 0 || m == 0 || n*m > maxWordDiffCells {
		return wholeWordSegs(oldLine), wholeWordSegs(newLine)
	}

	// Same backward DP and forward walk as diffMiddle, but over tokens. Using
	// the identical tie-break (delete on >=) keeps results stable and makes an
	// unchanged token align with the same neighbour as in the line diff.
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if oldToks[i] == newToks[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}

	oldChanged := make([]bool, n)
	newChanged := make([]bool, m)
	lcs := 0
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case oldToks[i] == newToks[j]:
			lcs++
			i, j = i+1, j+1
		case dp[i+1][j] >= dp[i][j+1]:
			oldChanged[i] = true
			i++
		default:
			newChanged[j] = true
			j++
		}
	}
	for ; i < n; i++ {
		oldChanged[i] = true
	}
	for ; j < m; j++ {
		newChanged[j] = true
	}

	// Degrade when more than half of the token occurrences changed, or when
	// one line is more than three times longer than the other. In either case
	// the token alignment is too weak to be a useful highlight.
	if (n+m-2*lcs)*2 > n+m || wordLengthSkew(oldLine, newLine) {
		return wholeWordSegs(oldLine), wholeWordSegs(newLine)
	}

	return mergeWordSegs(oldToks, oldChanged), mergeWordSegs(newToks, newChanged)
}

// wholeWordSegs marks an entire non-empty line as changed.
func wholeWordSegs(text string) []WordSegment {
	if text == "" {
		return nil
	}
	return []WordSegment{{Text: text, Changed: true}}
}

// mergeWordSegs concatenates neighbouring tokens carrying the same Changed
// flag so a one-word edit produces three spans, not one per token.
func mergeWordSegs(toks []string, changed []bool) []WordSegment {
	var out []WordSegment
	for i, tok := range toks {
		if tok == "" {
			continue
		}
		if len(out) > 0 && out[len(out)-1].Changed == changed[i] {
			out[len(out)-1].Text += tok
			continue
		}
		out = append(out, WordSegment{Text: tok, Changed: changed[i]})
	}
	return out
}

// wordLengthSkew reports whether one line is more than three times as long as
// the other. The comparison counts runes so CJK is not penalised for its byte
// width.
func wordLengthSkew(oldLine, newLine string) bool {
	a, b := utf8.RuneCountInString(oldLine), utf8.RuneCountInString(newLine)
	if a > b {
		a, b = b, a
	}
	return a*3 < b
}

type wordRuneClass uint8

const (
	classOther wordRuneClass = iota
	classWord
	classSpace
	classIdeograph
)

// wordTokens splits a line into lossless tokens: runs of letters/digits/_,
// runs of whitespace, runs of punctuation or symbols, and individual CJK
// ideographs. Ideographs are not whitespace-separated, so one rune per token
// gives a finer highlight while staying safe on UTF-8 byte boundaries.
func wordTokens(line string) []string {
	if line == "" {
		return nil
	}
	var toks []string
	start, class, first := 0, classOther, true
	for pos, r := range line {
		c := classifyWordRune(r)
		if first {
			start, class, first = pos, c, false
			continue
		}
		if c != class || class == classIdeograph {
			toks = append(toks, line[start:pos])
			start, class = pos, c
		}
	}
	toks = append(toks, line[start:])
	return toks
}

func classifyWordRune(r rune) wordRuneClass {
	switch {
	case r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
		if isIdeograph(r) {
			return classIdeograph
		}
		return classWord
	case unicode.IsSpace(r):
		return classSpace
	default:
		return classOther
	}
}

func isIdeograph(r rune) bool {
	return unicode.Is(unicode.Han, r) ||
		unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) ||
		unicode.Is(unicode.Hangul, r)
}
