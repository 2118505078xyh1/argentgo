package tui

import "strings"

// DiffKind 是行差异中某一行的角色。
type DiffKind uint8

// Diff 的行角色。
const (
	DiffContext DiffKind = iota
	DiffAdd
	DiffDel
)

// DiffLine 是行差异的一行。Text 从不携带 +/- 标记；
// 渲染器添加它，使同一辅助函数可复用于纯文本输出。
type DiffLine struct {
	Kind DiffKind
	Text string
}

// maxDiffLCSCells 限制动态规划表。超过此值时，块被
// 整体作为删除+新增输出，这仍然正确，只是不够紧凑。
const maxDiffLCSCells = 64 * 1024

// DiffLines 返回两段文本的上下文/新增/删除行差异。它先修剪
// 相同的的前缀和后缀，然后对中间部分运行一个小型 LCS；
// 两个阶段都只使用标准库。
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

// diffSplit 丢弃尾随换行本会添加的空元素。
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

// diffMiddle 对齐变化区域。超过上限时它降级为 del+add，
// 而不是分配无界的表。
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
