package jobs

import "sort"

// MergeQueues 将多个已按 (Priority 降序, Seq 升序) 排序的队列归并为一个稳定
// 有序的副本。相同 ID 只保留一项：优先保留 Priority 最高者；若 Priority 相同，
// 则保留 Seq 最大（即最新）者；两者都相同时保留最先出现的一项。
//
// 函数是纯函数：先按参数顺序把各队列平铺复制到新切片再排序，绝不修改入参，
// 返回的切片与所有入参都不共享底层数组。所有队列均为空（含 nil）时返回 nil。
// 相等键（Priority 与 Seq 都相同）的条目保持平铺输入中的相对顺序，即归并是
// 稳定的。零调用方的可观测辅助，不改变任何现有作业行为。
func MergeQueues(queues ...[]PriorityItem) []PriorityItem {
	total := 0
	for _, q := range queues {
		total += len(q)
	}
	if total == 0 {
		return nil
	}

	// 平铺所有队列：按参数顺序、队列内顺序拼接。append 从 nil 切片开始，
	// 得到的是独立副本，不会与各入参共享底层数组。
	all := make([]PriorityItem, 0, total)
	for _, q := range queues {
		all = append(all, q...)
	}

	// 稳定排序：键相同的条目保持平铺顺序，从而保证归并的稳定性。
	sort.SliceStable(all, func(i, j int) bool {
		return priorityLess(all[i], all[j])
	})

	// 选出每个 ID 的胜出项下标：Priority 更高者优先；Priority 相同则 Seq
	// 更大（更新）者优先；两者都相同则保留先出现者（即更靠前的下标）。
	winner := make(map[string]int, total)
	for i, item := range all {
		prev, ok := winner[item.ID]
		if !ok {
			winner[item.ID] = i
			continue
		}
		if item.Priority > all[prev].Priority ||
			(item.Priority == all[prev].Priority && item.Seq > all[prev].Seq) {
			winner[item.ID] = i
		}
	}

	// 按已排序的 all 顺序输出胜出项：整体天然有序，且等键条目的相对顺序
	// 与稳定排序结果一致。out 由 append 从 nil 构建，是独立副本。
	out := make([]PriorityItem, 0, len(winner))
	for i, item := range all {
		if winner[item.ID] == i {
			out = append(out, item)
		}
	}
	return out
}

// QueueDelta 按 ID 比较两批条目，返回新增（只出现在 after）与移除（只出现在
// before）的 ID 列表。同一 ID 即使 Priority 或 Seq 改变也不视为变化。
//
// 结果不含重复 ID，顺序与各自入参中的出现顺序一致（稳定）；返回的切片是独立
// 副本，绝不修改入参。两批都为空（含 nil）时返回两个 nil。支持 CJK（多字节）
// ID，因为比较仅基于字符串相等。零调用方的纯函数，不改变任何现有作业行为。
func QueueDelta(before, after []PriorityItem) (added, removed []string) {
	beforeIDs := make(map[string]struct{}, len(before))
	for _, item := range before {
		beforeIDs[item.ID] = struct{}{}
	}
	afterIDs := make(map[string]struct{}, len(after))
	for _, item := range after {
		afterIDs[item.ID] = struct{}{}
	}

	seenAdded := make(map[string]struct{})
	for _, item := range after {
		if _, present := beforeIDs[item.ID]; present {
			continue
		}
		if _, dup := seenAdded[item.ID]; dup {
			continue
		}
		seenAdded[item.ID] = struct{}{}
		added = append(added, item.ID)
	}

	seenRemoved := make(map[string]struct{})
	for _, item := range before {
		if _, present := afterIDs[item.ID]; present {
			continue
		}
		if _, dup := seenRemoved[item.ID]; dup {
			continue
		}
		seenRemoved[item.ID] = struct{}{}
		removed = append(removed, item.ID)
	}

	return added, removed
}
