package memory

// 关联召回排序。本文件提供一组纯函数，把已评分的候选按稳定的规则
// 排序、按标签过滤并按 ID 去重。它们不执行磁盘 I/O、不修改入参、
// 不调用任何模型，也不触碰 Bank 的默认召回行为；调用方可以自行
// 把它们接入自己的召回管线。

import (
	"math"
	"sort"
	"strings"
)

// RecallCandidate 是一条参与召回排序的候选。
type RecallCandidate struct {
	// ID 是候选的稳定标识，用作排序的最终决胜键与去重键。
	ID string
	// Score 是相关性分数，越大越靠前；NaN 视为最低分。
	Score float64
	// Recency 是新近度（例如时间戳），越大越靠前。
	Recency int64
	// Tags 是候选携带的标签，用于 RankOptions.TagFilter 过滤。
	Tags []string
}

// RankOptions 配置 RankRecall。零值有效：不做标签过滤。
type RankOptions struct {
	// TagFilter 非空时，只保留 Tags 含有其中任一标签的候选。
	// 标签比较不区分大小写（与 Bank.Recall 一致）。
	TagFilter []string
}

// RankRecall 按 (Score 降序, Recency 降序, ID 升序) 稳定排序 cands，
// 并在 opts.TagFilter 非空时先过滤掉不含其中任一标签的候选。
//
// 它是纯函数：不修改 cands，返回的新切片不与入参共享底层数组；
// NaN Score 视为最低分，排在一切非 NaN 分数之后，两个 NaN 视为相等。
// 完全相同的候选（分数、新近度与 ID 均相同）保持输入中的相对顺序。
// 结果为空时返回 nil。
func RankRecall(cands []RecallCandidate, opts RankOptions) []RecallCandidate {
	if len(cands) == 0 {
		return nil
	}

	out := make([]RecallCandidate, 0, len(cands))
	for _, c := range cands {
		if len(opts.TagFilter) > 0 && !hasAnyTag(c.Tags, opts.TagFilter) {
			continue
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil
	}

	sort.SliceStable(out, func(i, j int) bool {
		if c := compareScore(out[i].Score, out[j].Score); c != 0 {
			return c < 0 // 分数高者靠前
		}
		if out[i].Recency != out[j].Recency {
			return out[i].Recency > out[j].Recency // 新近者靠前
		}
		return out[i].ID < out[j].ID // ID 升序，保证确定性
	})
	return out
}

// DeduplicateRecall 按 ID 去重，每个 ID 只保留分数最高的候选。
// 分数相同时（含两个 NaN）保留首次出现者；输出按各 ID 首次出现的
// 顺序排列。它是纯函数：不修改 cands，结果为空时返回 nil。
func DeduplicateRecall(cands []RecallCandidate) []RecallCandidate {
	if len(cands) == 0 {
		return nil
	}

	type slot struct {
		cand RecallCandidate
	}
	best := make(map[string]*slot, len(cands))
	order := make([]string, 0, len(cands))
	for _, c := range cands {
		if s, ok := best[c.ID]; ok {
			// 仅在严格更高时替换，平局保留首次出现者。
			if compareScore(c.Score, s.cand.Score) < 0 {
				s.cand = c
			}
			continue
		}
		best[c.ID] = &slot{cand: c}
		order = append(order, c.ID)
	}

	out := make([]RecallCandidate, len(order))
	for i, id := range order {
		out[i] = best[id].cand
	}
	return out
}

// hasAnyTag 报告 tags 是否含有 want 中的任一标签（不区分大小写）。
func hasAnyTag(tags, want []string) bool {
	for _, w := range want {
		for _, t := range tags {
			if strings.EqualFold(w, t) {
				return true
			}
		}
	}
	return false
}

// compareScore 比较两个分数在排序中的先后：a 应排在 b 前返回 -1，
// 相等返回 0，排在后面返回 1。NaN 视为最低分，排在一切非 NaN 之后，
// 两个 NaN 视为相等。
func compareScore(a, b float64) int {
	if scoreLess(a, b) {
		return 1
	}
	if scoreLess(b, a) {
		return -1
	}
	return 0
}

// scoreLess 报告 a 的分数是否严格低于 b；NaN 严格低于任何非 NaN，
// 但两个 NaN 相等。
func scoreLess(a, b float64) bool {
	aNaN, bNaN := math.IsNaN(a), math.IsNaN(b)
	if aNaN || bNaN {
		return aNaN && !bNaN
	}
	return a < b
}
