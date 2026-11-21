package jobs

import "sort"

// Priority 是优先级级别的命名类型。它作为公开的、基于 int 的类型存在，
// 供调用方以类型化的方式表达优先级；PriorityItem.Priority 仍使用 int，
// 二者通过显式转换即可互操作。声明本类型不改变任何现有行为。
type Priority int

// PriorityItem 是优先级选择原语所需的最小视图：ID 标识条目，Priority 越大
// 越优先，Seq 是同优先级内决定先后顺序的单调递增序号。
type PriorityItem struct {
	ID       string
	Priority int
	Seq      uint64
}

// SelectByPriority 按 (Priority 降序, Seq 升序) 稳定排序后取前 limit 个条目。
//
// 排序是稳定的：当 Priority 与 Seq 都相同时，保留它们在入参中的相对顺序。
// 它是纯函数：先复制入参再排序，绝不修改 items，返回的切片与入参不共享底层
// 数组。limit<=0 或入参为空时返回 nil；limit 大于条目数时返回全部（已排序）
// 条目。
func SelectByPriority(items []PriorityItem, limit int) []PriorityItem {
	if len(items) == 0 || limit <= 0 {
		return nil
	}
	sorted := make([]PriorityItem, len(items))
	copy(sorted, items)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.Priority != b.Priority {
			return a.Priority > b.Priority // 优先级降序
		}
		return a.Seq < b.Seq // 同优先级按序号升序
	})
	if limit < len(sorted) {
		sorted = sorted[:limit]
	}
	return sorted
}

// PrioritySummary 统计各优先级出现的次数，返回以优先级值为键的计数表。
//
// 它是纯函数：不修改入参，也不读取任何全局状态。入参为空时返回空的
// （非 nil）映射，便于调用方直接读取而不必判空。
func PrioritySummary(items []PriorityItem) map[int]int {
	out := make(map[int]int, len(items))
	for _, item := range items {
		out[item.Priority]++
	}
	return out
}
