package memory

// 关联召回的可排序入口。RecallRanked 是 Bank 的一个新方法：
// 它复用与 Recall 完全相同的加载、过滤与计分规则，把命中映射为
// RecallCandidate 后交给 RankRecall 排序，从而给 recall_rank.go 中的
// 纯函数提供一个真实调用方。它不触碰 Recall 的任何一行代码。

import "strings"

// RecallRanked 是 Recall 的排序版替代：给定相同的 query/tags/limit，
// 返回的内存集合与顺序都与 Recall 一致，但排序实际由 RankRecall 完成。
//
// 与 Recall 保持一致的要点：
//   - 已作废（Invalidated）的事实同样被排除；
//   - 只有 query 标记或标签命中才参与过滤，空 query 且无标签时全部返回；
//   - 标签匹配不区分大小写；相关性计分规则与 Recall 完全相同；
//   - 非正数 limit 使用 defaultRecallLimit。
//
// 刻意不传 RankOptions.TagFilter：标签过滤已在计分阶段完成，若让
// RankRecall 再做一次标签过滤，空 query + 标签的召回会发生行为漂移
// （Recall 中标签是通过加分参与的，而非硬性剔除无标签项）。
//
// 命中到 RecallCandidate 的映射：
//   - Score = 召回相关性分数（非负整数，不会出现 NaN）；
//   - Recency = 命中在日志中的行号。仅追加日志中行号越大越新，用单一
//     int64 即可精确复现 Recall 中“时间戳降序、再行号降序”的决胜逻辑；
//     时间戳与行号无法无损合并进一个 int64（乘倍数会溢出、加低位的差值
//     在高精度时钟下会被行号盖过），故直接用行号作为新近度代理；
//   - ID = 内存 ID，仅用于把排序结果回填为 Memory；
//   - Tags = 内存标签，保留原样（不参与二次过滤）。
//
// 由于行号唯一且 Score 非负，RankRecall 的 (Score, Recency, ID) 三级排序
// 中 ID 决胜键不会触发，因此输出顺序与 Recall 的 sort.SliceStable 一致。
func (b *Bank) RecallRanked(query string, tags []string, limit int) []Memory {
	b.mu.Lock()
	all, err := b.loadLocked()
	b.mu.Unlock()
	if len(all) == 0 {
		return nil
	}
	_ = err // 与 Recall 一致：截断的尾部忽略，部分结果仍然有用

	if limit <= 0 {
		limit = defaultRecallLimit
	}
	queryTokens := tokenize(query)
	wantTags := cleanTags(tags)
	filtered := len(queryTokens) > 0 || len(wantTags) > 0

	type hit struct {
		mem   Memory
		score int
		idx   int // 日志行号，作为新近度代理
	}
	hits := make([]hit, 0, len(all))
	for i, mem := range all {
		// 与 Recall 一致：作废事实是墓碑，从不出现在召回结果中。
		if mem.Invalidated != "" {
			continue
		}
		score := 0
		if len(queryTokens) > 0 {
			textTokens := tokenize(mem.Text)
			for _, q := range queryTokens {
				for _, tok := range textTokens {
					if q == tok {
						score++
						break
					}
				}
			}
		}
		if len(wantTags) > 0 {
			for _, want := range wantTags {
				for _, tag := range mem.Tags {
					if strings.EqualFold(want, tag) {
						score += tagMatchBonus
						break
					}
				}
			}
		}
		if filtered && score == 0 {
			continue
		}
		hits = append(hits, hit{mem: mem, score: score, idx: i})
	}

	byID := make(map[string]Memory, len(hits))
	cands := make([]RecallCandidate, 0, len(hits))
	for _, h := range hits {
		byID[h.mem.ID] = h.mem
		cands = append(cands, RecallCandidate{
			ID:      h.mem.ID,
			Score:   float64(h.score),
			Recency: int64(h.idx),
			Tags:    h.mem.Tags,
		})
	}

	// 零值 RankOptions：不做二次标签过滤。
	ranked := RankRecall(cands, RankOptions{})
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	out := make([]Memory, len(ranked))
	for i, c := range ranked {
		out[i] = byID[c.ID]
	}
	return out
}
