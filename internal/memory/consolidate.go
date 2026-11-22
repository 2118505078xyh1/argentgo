package memory

import (
	"sort"
	"strings"
)

// 本文件实现记忆库的"睡眠整合"：把语义近重复的事实合并为一条，合并来源，
// 并按新近度排序。它是 omp/mnemopi 的 Consolidate 思想的纯 stdlib 自研实现。
// 默认不运行——只有显式调用工具才会改写记忆日志，未使用时库字节逐字不变。

// ConsolidationStats 描述一次整合的结果，供工具回显给用户。
type ConsolidationStats struct {
	Before int `json:"before"`
	After  int `json:"after"`
	Merged int `json:"merged"`
}

// consolidateTokenLimit 限制参与相似度计算的标记数，防止超长条目拖慢整合。
const consolidateTokenLimit = 256

// consolidateThreshold 是判定"近重复"的 Jaccard 相似度阈值。
const consolidateThreshold = 0.8

// Consolidate 把 entries 中的近重复事实合并。相同/近似文本的条目聚为一簇，
// 簇内保留最新 TS、合并去重后的 Source（逗号分隔），Tags 取并集。
// 返回新切片，不修改入参；Invalidated 条目原样保留（不参与合并）。
func Consolidate(entries []Memory) ([]Memory, ConsolidationStats) {
	stats := ConsolidationStats{Before: len(entries)}
	if len(entries) == 0 {
		return nil, stats
	}
	tokens := make([][]string, len(entries))
	for i, e := range entries {
		tokens[i] = consolidateTokens(e.Text)
	}

	used := make([]bool, len(entries))
	out := make([]Memory, 0, len(entries))
	for i := range entries {
		if used[i] {
			continue
		}
		used[i] = true
		merged := entries[i]
		if merged.Invalidated == "" {
			for j := i + 1; j < len(entries); j++ {
				if used[j] || entries[j].Invalidated != "" {
					continue
				}
				if jaccard(tokens[i], tokens[j]) >= consolidateThreshold {
					merged = mergeMemory(merged, entries[j])
					used[j] = true
					stats.Merged++
				}
			}
		}
		out = append(out, merged)
	}
	// 新近优先：TS 相同时按 ID 保证稳定顺序。
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].TS.Equal(out[j].TS) {
			return out[i].TS.After(out[j].TS)
		}
		return out[i].ID < out[j].ID
	})
	stats.After = len(out)
	return out, stats
}

// mergeMemory 把 b 合并进 a：保留更新的 TS，合并去重后的来源与标签。
func mergeMemory(a, b Memory) Memory {
	if b.TS.After(a.TS) {
		a.TS = b.TS
	}
	a.Source = mergeList(a.Source, b.Source)
	a.Tags = mergeTags(a.Tags, b.Tags)
	return a
}

func mergeList(a, b string) string {
	parts := strings.Split(a, ",")
	seen := make(map[string]bool, len(parts))
	for _, p := range parts {
		seen[strings.TrimSpace(p)] = true
	}
	for _, p := range strings.Split(b, ",") {
		p = strings.TrimSpace(p)
		if p != "" && !seen[p] {
			parts = append(parts, p)
			seen[p] = true
		}
	}
	return strings.Join(parts, ",")
}

func mergeTags(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, tag := range append(append([]string(nil), a...), b...) {
		if tag != "" && !seen[tag] {
			seen[tag] = true
			out = append(out, tag)
		}
	}
	return out
}

func consolidateTokens(text string) []string {
	fields := strings.Fields(strings.ToLower(text))
	if len(fields) > consolidateTokenLimit {
		fields = fields[:consolidateTokenLimit]
	}
	return fields
}

func jaccard(a, b []string) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 1
	}
	setA := make(map[string]bool, len(a))
	for _, t := range a {
		setA[t] = true
	}
	inter := 0
	setB := make(map[string]bool, len(b))
	for _, t := range b {
		setB[t] = true
		if setA[t] {
			inter++
		}
	}
	union := len(setA) + len(setB) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// ConsolidateBank 对整库做一次整合，原子写回。仅在显式调用时改写磁盘。
func (b *Bank) ConsolidateBank() (ConsolidationStats, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	entries, err := b.loadLocked()
	if err != nil {
		return ConsolidationStats{}, err
	}
	merged, stats := Consolidate(entries)
	if stats.Merged == 0 {
		return stats, nil // 无可合并项：不写盘，字节不变。
	}
	if err := b.writeAllLocked(merged); err != nil {
		return ConsolidationStats{}, err
	}
	return stats, nil
}
