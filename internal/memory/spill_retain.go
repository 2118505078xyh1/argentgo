package memory

// 溢出回收决策原语。PlanSpillRetention 把一份溢出工件清单划分为保留与
// 淘汰两组：按 LastUsed 升序淘汰最旧工件，但 pinned 中的工件永不淘汰；
// 先施加数量上限，再施加总字节上限，已被数量约束淘汰的工件不再计入字节
// 约束。它是纯函数：零 I/O、无调用方、不修改入参，也不改变现有落盘/恢复/
// 清理行为。

import "sort"

// SpillFile 是参与回收决策的一个溢出工件视图。
// Name 为工件文件名，Size 为字节数，LastUsed 为最近使用时间（Unix 秒）。
type SpillFile struct {
	Name     string
	Size     int64
	LastUsed int64
}

// RetainDecision 是一次回收决策的结果。Keep 与 Evict 都是工件文件名列表，
// 均按 LastUsed 升序排列（稳定排序：时间相同者保持输入中的相对顺序）。
// Evict 的顺序即淘汰顺序，从最旧到最新。
type RetainDecision struct {
	Keep  []string
	Evict []string
}

// PlanSpillRetention 规划溢出工件的保留/淘汰决策。
//
// 规则：
//   - 先复制 files 并按 LastUsed 升序稳定排序，绝不修改入参；
//   - 数量约束：maxCount>0 时保留总数不超过 maxCount。pinned 工件始终保留
//     并占用名额，故可保留的非 pinned 工件数为 maxCount 减去钉住数；超出的
//     最旧非 pinned 工件被淘汰；
//   - 字节约束：随后在 maxBytes>0 时计算仍保留工件的字节总和，若超过
//     maxBytes，则继续从最旧的未淘汰非 pinned 工件开始淘汰，直到达标或再无
//     可淘汰者（钉住工件即便导致超标也保留）；已淘汰的工件不再计入总和；
//   - 两个上限都 <=0 时全部保留；结果为空的分组返回 nil。
//
// stats 来自 AnalyzeSpillDir，仅作为决策上下文（供调用方一并记录/展示），
// 决策本身完全基于 files，因此 stats 为零值或与 files 不一致时行为依然确定。
func PlanSpillRetention(stats SpillStats, files []SpillFile, maxCount int, maxBytes int64, pinned map[string]bool) RetainDecision {
	_ = stats // 上下文参数，决策以 files 为准

	if len(files) == 0 {
		return RetainDecision{}
	}

	// 复制后稳定排序：LastUsed 相同者保持输入中的相对顺序。
	ordered := make([]SpillFile, len(files))
	copy(ordered, files)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].LastUsed < ordered[j].LastUsed
	})

	// evicted[i] 标记 ordered[i] 是否已淘汰。
	evicted := make([]bool, len(ordered))

	// 第一关：数量约束。
	if maxCount > 0 {
		pinnedCount := 0
		for _, f := range ordered {
			if pinned[f.Name] {
				pinnedCount++
			}
		}
		allowed := maxCount - pinnedCount
		if allowed < 0 {
			allowed = 0 // 钉住工件已超出上限：淘汰全部非钉住工件
		}
		toEvict := (len(ordered) - pinnedCount) - allowed
		for i := 0; i < len(ordered) && toEvict > 0; i++ {
			if evicted[i] || pinned[ordered[i].Name] {
				continue
			}
			evicted[i] = true
			toEvict--
		}
	}

	// 第二关：字节约束。已淘汰的不再计入，钉住的永不淘汰。
	if maxBytes > 0 {
		var total int64
		for i, f := range ordered {
			if !evicted[i] {
				total += f.Size
			}
		}
		for i := 0; i < len(ordered) && total > maxBytes; i++ {
			if evicted[i] || pinned[ordered[i].Name] {
				continue
			}
			evicted[i] = true
			total -= ordered[i].Size
		}
	}

	var keep, evict []string
	for i, f := range ordered {
		if evicted[i] {
			evict = append(evict, f.Name)
		} else {
			keep = append(keep, f.Name)
		}
	}
	return RetainDecision{Keep: keep, Evict: evict}
}
