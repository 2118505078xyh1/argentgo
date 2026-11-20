package tui

import "strings"

// DiffKind is the role of one row in a line diff.
type DiffKind uint8

// Diff row roles.
const (
	DiffContext DiffKind = iota
	DiffAdd
	DiffDel
)

// DiffLine is one row of a line diff. Text never carries the +/- marker; the
// renderer adds it so the same helper can be reused by plain-text output.
type DiffLine struct {
	Kind DiffKind
	Text string
}

// maxDiffLCSCells bounds the dynamic-programming table. Beyond this the hunk is
// emitted as a wholesale delete+add, which is still correct, just less tight.
const maxDiffLCSCells = 64 * 1024

// DiffLines returns a context/add/delete line diff of two texts. It trims the
// identical prefix and suffix first, then runs a small LCS over the middle;
// both stages use only the standard library.
func DiffLines(oldText, newText string) []DiffLine {
	oldLines, newLines := diffSplit(oldText), diffSplit(newText)

	prefix := 0
	for prefix < len(oldLines) && prefix < len(newLines) && oldLines[prefix] == newLines[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(oldLines)-prefix && suffix < len(newLines)-prefix &&
		oldLines[len(oldLines)-1-suffix] == newLines[len(newLines)-1-suffix] {
		suffix++
	}

	out := make([]DiffLine, 0, len(oldLines)+len(newLines))
	for _, line := range oldLines[:prefix] {
		out = append(out, DiffLine{Kind: DiffContext, Text: line})
	}
	out = append(out, diffMiddle(oldLines[prefix:len(oldLines)-suffix], newLines[prefix:len(newLines)-suffix])...)
	for _, line := range oldLines[len(oldLines)-suffix:] {
		out = append(out, DiffLine{Kind: DiffContext, Text: line})
	}
	return out
}

// diffSplit drops the empty element a trailing newline would otherwise add.
func diffSplit(text string) []string {
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// diffMiddle aligns the changed region. Over the cap it degrades to del+add
// rather than allocating an unbounded table.
func diffMiddle(oldLines, newLines []string) []DiffLine {
	n, m := len(oldLines), len(newLines)
	out := make([]DiffLine, 0, n+m)
	if n == 0 {
		for _, line := range newLines {
			out = append(out, DiffLine{Kind: DiffAdd, Text: line})
		}
		return out
	}
	if m == 0 {
		for _, line := range oldLines {
			out = append(out, DiffLine{Kind: DiffDel, Text: line})
		}
		return out
	}
	if n*m > maxDiffLCSCells {
		for _, line := range oldLines {
			out = append(out, DiffLine{Kind: DiffDel, Text: line})
		}
		for _, line := range newLines {
			out = append(out, DiffLine{Kind: DiffAdd, Text: line})
		}
		return out
	}

	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if oldLines[i] == newLines[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}

	i, j := 0, 0
	for i < n && j < m {
		switch {
		case oldLines[i] == newLines[j]:
			out = append(out, DiffLine{Kind: DiffContext, Text: oldLines[i]})
			i, j = i+1, j+1
		case dp[i+1][j] >= dp[i][j+1]:
			out = append(out, DiffLine{Kind: DiffDel, Text: oldLines[i]})
			i++
		default:
			out = append(out, DiffLine{Kind: DiffAdd, Text: newLines[j]})
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, DiffLine{Kind: DiffDel, Text: oldLines[i]})
	}
	for ; j < m; j++ {
		out = append(out, DiffLine{Kind: DiffAdd, Text: newLines[j]})
	}
	return out
}
