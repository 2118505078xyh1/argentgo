package cli

import (
	"sort"
	"time"
)

// ProjectEntry 是最近项目列表的一条候选记录：Path 为项目路径，
// LastAccessed 为该路径的最近访问时间。它由调用方构造后交给
// recentProjects 计算 LRU 视图，因此本包不依赖任何具体的存储实现。
type ProjectEntry struct {
	Path         string
	LastAccessed time.Time
}

// recentProjects 从 entries 计算最近访问的至多 max 个不重复项目路径，
// 按 LastAccessed 降序（最近优先）返回。它是一个纯函数：不读文件、不访问
// 终端、不触碰全局状态，只依赖参数，因此既能安全地用于表驱动测试，也能在
// 将来由真实的「最近访问记录」来源 opt-in 调用（当前尚未接线，见接线点报告）。
//
// 行为约定：
//   - 先按路径去重：同一路径只保留 LastAccessed 最新的一条参与排序；
//   - 再按 LastAccessed 降序排序并截取前 max 个，实现 LRU（最近使用优先）；
//   - 排序稳定：访问时间相同的路径保持其在输入中的先后次序；
//   - 路径作为完整字符串比较与返回，不按字节切片，故 CJK 等多字节路径安全；
//   - max <= 0 或输入为空时返回 nil（长度为 0）。
func recentProjects(entries []ProjectEntry, max int) []string {
	if max <= 0 || len(entries) == 0 {
		return nil
	}

	// latest 按路径保留最近访问的一条；order 记录每个路径首次出现的下标，
	// 用作访问时间相同时的稳定次序依据（不随去重更新）。
	latest := make(map[string]ProjectEntry, len(entries))
	order := make(map[string]int, len(entries))
	for i, e := range entries {
		if e.Path == "" {
			// 空路径不是有效的项目路径，直接忽略。
			continue
		}
		if _, seen := order[e.Path]; !seen {
			order[e.Path] = i
		}
		prev, ok := latest[e.Path]
		if !ok || e.LastAccessed.After(prev.LastAccessed) {
			latest[e.Path] = e
		}
	}

	ranked := make([]ProjectEntry, 0, len(latest))
	for _, e := range latest {
		ranked = append(ranked, e)
	}
	// 先按访问时间降序；时间相同则按输入首次出现的下标升序，保证结果稳定可复现。
	sort.SliceStable(ranked, func(i, j int) bool {
		a, b := ranked[i], ranked[j]
		if !a.LastAccessed.Equal(b.LastAccessed) {
			return a.LastAccessed.After(b.LastAccessed)
		}
		return order[a.Path] < order[b.Path]
	})
	if len(ranked) > max {
		ranked = ranked[:max]
	}
	out := make([]string, len(ranked))
	for i, e := range ranked {
		out[i] = e.Path
	}
	return out
}
