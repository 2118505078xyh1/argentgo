package cli

import (
	"strings"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/tui"
)

// fuzzyFindSessions 在 titles 中查找与 query 模糊匹配的条目，返回它们在
// titles 中的原始下标，按相关性从高到低排列；未匹配的条目被排除。
//
// 匹配逻辑复用 tui.FuzzyMatch：先把 query 折叠成小写，再借助 FuzzyMatch 的
// 智能大小写路径实现不区分大小写的子序列匹配，同时保留原始标题里的
// camelCase/分隔符，让词边界加成照常生效。所有索引都是 rune 索引，因此
// 处理 CJK/emoji 等多字节文本时是安全的。
//
// 排序复用 tui.SortFuzzyResults：得分高者优先，其次文本更短、首次匹配更靠前，
// 完全相同的条目按原始下标升序，因此结果稳定且确定。
//
// 空（或仅空白）的 query 没有相关性信号，此时按原始顺序返回全部下标。
func fuzzyFindSessions(query string, titles []string) []int {
	if strings.TrimSpace(query) == "" {
		indices := make([]int, len(titles))
		for i := range indices {
			indices[i] = i
		}
		return indices
	}

	// 小写 query 触发 FuzzyMatch 的折叠分支，从而对原始标题做大小写不敏感匹配。
	needle := strings.ToLower(query)

	candidates := make([]fuzzyFindCandidate, 0, len(titles))
	for i, title := range titles {
		score, positions, ok := tui.FuzzyMatch(needle, title, false)
		if !ok {
			continue
		}
		candidates = append(candidates, fuzzyFindCandidate{
			originalIndex: i,
			score:         score,
			positions:     positions,
			textLen:       utf8.RuneCountInString(title),
		})
	}

	ranked := make([]tui.FuzzyResult, len(candidates))
	for i, candidate := range candidates {
		ranked[i] = tui.FuzzyResult{
			Score:     candidate.score,
			Positions: candidate.positions,
			TextLen:   candidate.textLen,
			Index:     i,
		}
	}
	tui.SortFuzzyResults(ranked)

	indices := make([]int, len(ranked))
	for i, result := range ranked {
		indices[i] = candidates[result.Index].originalIndex
	}
	return indices
}

// fuzzyFindCandidate 保存一个匹配条目及其在原始 titles 中的下标。
type fuzzyFindCandidate struct {
	originalIndex int
	score         int
	positions     []int
	textLen       int
}
