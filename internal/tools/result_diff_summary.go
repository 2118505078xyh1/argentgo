package tools

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

const (
	// diffSummaryPreviewLines 限制预览最多展示的文件行数。
	diffSummaryPreviewLines = 4
	// diffSummaryPathRunes 限制单条预览中路径的最大 rune 数（含省略号预留）。
	diffSummaryPathRunes = 40
	// diffSummaryMaxLCSLines 是 LCS 的行数上限；超出则回退到整文件计数，
	// 避免为超大输入分配平方级的表。
	diffSummaryMaxLCSLines = 2000
)

// DiffSummary 是编辑类工具结果的紧凑结构化摘要。
// Added/Removed 为所有变更文件累计的新增/删除行数，
// Files 为发生变化的文件数，Preview 为最多几行的可读预览。
type DiffSummary struct {
	Added   int
	Removed int
	Files   int
	Preview string
}

// SummarizeEditResult 对比编辑前后的文件快照，生成有界的 diff 摘要。
// before/after 的键为文件路径，值为文件完整内容。仅内容发生变化的文件
// （新增、删除或修改）才计入 Files 与行数统计；内容相同的文件被忽略。
//
// 本函数是纯函数：不修改入参；输出只取决于输入内容，与 map 遍历顺序无关，
// 对同一输入恒定。行统计采用有界 LCS，使未变化的行不被计为编辑；
// CRLF 归一化为 LF，因此仅行尾差异不会夸大结果。
// 本文件自包含（仅依赖标准库），不复用 DiffLineCounts/formatAwareDiff，
// 以免与同包其它文件形成耦合或依赖环。
func SummarizeEditResult(before, after map[string]string) DiffSummary {
	var s DiffSummary

	// 收集两幅快照中出现过的全部路径并排序，消除 map 遍历顺序的随机性。
	paths := make([]string, 0, len(before)+len(after))
	seen := make(map[string]struct{}, len(before)+len(after))
	for p := range before {
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		paths = append(paths, p)
	}
	for p := range after {
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		paths = append(paths, p)
	}
	sort.Strings(paths)

	preview := make([]string, 0, diffSummaryPreviewLines)
	for _, p := range paths {
		oldContent, inBefore := before[p]
		newContent, inAfter := after[p]
		status := "~" // 默认：两幅快照都存在但内容不同
		var added, removed int
		switch {
		case inBefore && inAfter:
			if oldContent == newContent {
				continue // 内容未变，不计入摘要
			}
			added, removed = diffSummaryLineCounts([]byte(oldContent), []byte(newContent))
		case inAfter:
			status = "+"
			added = len(diffSummarySplitLines([]byte(newContent)))
		default:
			status = "-"
			removed = len(diffSummarySplitLines([]byte(oldContent)))
		}

		s.Added += added
		s.Removed += removed
		s.Files++

		if len(preview) < diffSummaryPreviewLines {
			preview = append(preview, fmt.Sprintf("%s %s (+%d/-%d)",
				status, diffSummaryTruncatePath(p, diffSummaryPathRunes), added, removed))
		}
	}
	s.Preview = strings.Join(preview, "\n")
	return s
}

// diffSummarySplitLines 把内容切成行：CRLF 归一化为 LF，去掉末尾的换行，
// 空内容返回 nil。与 splitDiffLines 思路一致，但在本文件内自包含。
func diffSummarySplitLines(data []byte) []string {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// diffSummaryLineCounts 返回 before 与 after 之间的 (新增, 删除) 行数。
// 使用有界 LCS，使未变化的行不被计为编辑；超大输入回退到整文件计数，
// 避免为平方级的表分配内存。
func diffSummaryLineCounts(before, after []byte) (added, removed int) {
	a := diffSummarySplitLines(before)
	b := diffSummarySplitLines(after)
	if len(a) > diffSummaryMaxLCSLines || len(b) > diffSummaryMaxLCSLines ||
		len(a)*len(b) > diffSummaryMaxLCSLines*diffSummaryMaxLCSLines {
		return len(b), len(a)
	}
	dp := make([][]int, len(a)+1)
	for i := range dp {
		dp[i] = make([]int, len(b)+1)
	}
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			if a[i-1] == b[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
			} else if dp[i-1][j] >= dp[i][j-1] {
				dp[i][j] = dp[i-1][j]
			} else {
				dp[i][j] = dp[i][j-1]
			}
		}
	}
	common := dp[len(a)][len(b)]
	return len(b) - common, len(a) - common
}

// diffSummaryTruncatePath 在字素簇边界把路径截断到最多 maxRunes 个 rune，
// 超出时追加 "…"。使用 []rune 迭代，因此不会切坏 CJK 等宽字符；
// 同时避免把 Unicode 组合标记与其基字符拆开，确保输出为合法 UTF-8。
func diffSummaryTruncatePath(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	budget := maxRunes - 1 // 为省略号预留一个 rune
	cut := budget
	// 切点不能落在组合标记之前，否则会把一个字素簇拆成两半。
	for cut > 0 && diffSummaryIsCombining(runes[cut]) {
		cut--
	}
	return string(runes[:cut]) + "…"
}

// diffSummaryIsCombining 报告 r 是否为 Unicode 组合标记（Mn/Mc/Me）。
func diffSummaryIsCombining(r rune) bool {
	return unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Mc, r) || unicode.Is(unicode.Me, r)
}
