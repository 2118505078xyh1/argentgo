package jobs

// PrioritySnapshot 是优先队列在某一时刻的只读视图。
//
// Total 是条目总数，ByPriority 是按优先级值统计的计数表，Head 与 Tail 分别是
// 排序后（Priority 降序、Seq 升序）的前 headN 项与后 tailN 项。Head、Tail 都是
// 独立副本，彼此不共享底层数组，也不与构造入参共享底层数组。
type PrioritySnapshot struct {
	Total      int
	ByPriority map[int]int
	Head       []PriorityItem
	Tail       []PriorityItem
}

// SnapshotItems 由一组条目构造优先级快照，是纯函数：先复制入参再排序，绝不
// 修改 items，返回的 Head/Tail 与入参不共享底层数组。
//
// 当 items 为空（含 nil）时返回零值快照（各字段均为零值/nil）。headN、tailN
// 小于 1 时对应的 Head/Tail 为 nil；大于条目数时取全部（Head 与 Tail 可能重叠
// 覆盖同一批条目，但二者彼此仍是独立副本）。
func SnapshotItems(items []PriorityItem, headN, tailN int) PrioritySnapshot {
	if len(items) == 0 {
		return PrioritySnapshot{}
	}

	sorted := SortedByPriority(items)
	return PrioritySnapshot{
		Total:      len(items),
		ByPriority: PrioritySummary(items),
		Head:       snapshotPrefix(sorted, headN),
		Tail:       snapshotSuffix(sorted, tailN),
	}
}

// SnapshotQueue 构造优先队列当前内容的优先级快照。q 为 nil 时返回零值快照。
//
// 本函数不修改队列：它借助队列自带的 Snapshot 取得有序副本，再交由纯函数
// SnapshotItems 完成统计与截取。零调用方的可观测辅助，不改变任何现有作业行为。
func SnapshotQueue(q *priorityQueue, headN, tailN int) PrioritySnapshot {
	if q == nil {
		return PrioritySnapshot{}
	}
	return SnapshotItems(q.Snapshot(), headN, tailN)
}

// snapshotPrefix 返回 src 的前 n 项独立副本。n<=0 或 src 为空时返回 nil，
// n 超过长度时取全部。
func snapshotPrefix(src []PriorityItem, n int) []PriorityItem {
	if n <= 0 || len(src) == 0 {
		return nil
	}
	if n > len(src) {
		n = len(src)
	}
	out := make([]PriorityItem, n)
	copy(out, src[:n])
	return out
}

// snapshotSuffix 返回 src 的后 n 项独立副本。n<=0 或 src 为空时返回 nil，
// n 超过长度时取全部。
func snapshotSuffix(src []PriorityItem, n int) []PriorityItem {
	if n <= 0 || len(src) == 0 {
		return nil
	}
	if n > len(src) {
		n = len(src)
	}
	out := make([]PriorityItem, n)
	copy(out, src[len(src)-n:])
	return out
}
