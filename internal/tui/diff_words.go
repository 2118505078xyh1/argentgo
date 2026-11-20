package tui

import (
	"unicode"
	"unicode/utf8"
)

// WordSegment 是词差异某一侧上的连续文本段。Changed
// 表示该段在另一侧没有对应项。把所有段的
// Text 连接起来可精确还原原始行，包括空白和
// 标点。
type WordSegment struct {
	Text    string
	Changed bool
}

// maxWordDiffCells 限制 token 的 LCS 表，与 diff.go 中的
// maxDiffLCSCells 一致。这么大的行对不值得逐 token 对齐。
const maxWordDiffCells = 64 * 1024

// WordDiff 以 token 粒度对齐一对 -/+ 行，并返回
// 两侧各自的唯一段。小改动只高亮涉及的词；
// 松散相关的行降级为每侧一个整行 Changed 段，
// 而不是产生一堆令人分心的彩色碎片。
// 空输入不产生任何段。
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

	// 与 diffMiddle 相同的反向 DP 和正向遍历，但基于 token。使用
	// 相同的决胜规则（>= 时删除）使结果稳定，并使
	// 未改变的 token 与行差异中相同的邻居对齐。
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

	// 当超过一半的 token 出现发生变化，或
	// 某一行比另一行长三倍以上时降级。这两种情况下
	// token 对齐都太弱，无法作为有用的高亮。
	if (n+m-2*lcs)*2 > n+m || wordLengthSkew(oldLine, newLine) {
		return wholeWordSegs(oldLine), wholeWordSegs(newLine)
	}

	return mergeWordSegs(oldToks, oldChanged), mergeWordSegs(newToks, newChanged)
}

// wholeWordSegs 把整个非空行标记为已改变。
func wholeWordSegs(text string) []WordSegment {
	if text == "" {
		return nil
	}
	return []WordSegment{{Text: text, Changed: true}}
}

// mergeWordSegs 合并带有相同 Changed 标志的相邻
// token，使一个单词的编辑产生三个 span，而不是每个 token 一个。
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

// wordLengthSkew 报告某一行是否比另一行长三倍以上。
// 该比较按 rune 计数，使 CJK 不会因其字节
// 宽度而吃亏。
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

// wordTokens 把一行切分为无损 token：字母/数字/_ 的连续段、
// 空白的连续段、标点或符号的连续段，以及单独的 CJK
// 表意文字。表意文字不由空白分隔，因此每个 rune 一个 token
// 可提供更细的高亮，同时保持 UTF-8 字节边界安全。
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
