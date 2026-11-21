package session

import "sort"

// ContextDelta 描述两个上下文源快照之间按 key 对比得到的差异。
//
// Added 是只在 after 中出现的 key，Removed 是只在 before 中出现的 key，
// Changed 是两边都有但对应值不同的 key；三个切片都按 key 的字典序排列。
// unchanged 是两边都有且值完全相同的 key 数量（计入总数但不进入任何切片）。
type ContextDelta struct {
	Added     []string
	Removed   []string
	Changed   []string
	unchanged int
}

// Unchanged 返回值未发生变化的 key 数量。
//
// unchanged 字段刻意不导出：差异分类只暴露三种变化的 key，调用方通过本方法读取
// 未变化的计数，用于诊断与可观测汇总。
func (d ContextDelta) Unchanged() int { return d.unchanged }

// DiffContextSnapshots 按 key 对比两个上下文源快照，把差异归类为新增/删除/修改。
//
// before 与 after 都是“带 key 的来源”到其上下文文本的映射。判定只看 key 是否存在
// 以及对应的值是否相等：
//   - key 只在 after 中出现 → Added；
//   - key 只在 before 中出现 → Removed；
//   - 两边都有但值不同 → Changed；
//   - 两边都有且值相同 → 计入 unchanged，不进入任何切片。
//
// 空字符串是合法值：key 存在但值为空，与 key 不存在是两回事，不能混为一类。
//
// 三个切片在返回前都经 sort.Strings 排序，map 的随机遍历顺序不会泄漏到结果中，
// 因此相同入参总得到相同结果（确定性）。排序按 Go 字符串的 UTF-8 字节序进行，
// 等价于按 Unicode 码点序，CJK key 会被原样保留且稳定排序，不会被截断成半个码点。
//
// 纯函数：只读入参、返回全新的切片，不修改 before / after；nil map 当作空快照处理。
func DiffContextSnapshots(before, after map[string]string) ContextDelta {
	delta := ContextDelta{
		Added:   make([]string, 0),
		Removed: make([]string, 0),
		Changed: make([]string, 0),
	}

	for key, oldValue := range before {
		newValue, ok := after[key]
		switch {
		case !ok:
			delta.Removed = append(delta.Removed, key)
		case oldValue != newValue:
			delta.Changed = append(delta.Changed, key)
		default:
			delta.unchanged++
		}
	}
	for key := range after {
		if _, ok := before[key]; !ok {
			delta.Added = append(delta.Added, key)
		}
	}

	sort.Strings(delta.Added)
	sort.Strings(delta.Removed)
	sort.Strings(delta.Changed)
	return delta
}
