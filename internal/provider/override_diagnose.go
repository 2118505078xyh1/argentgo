package provider

import (
	"fmt"
	"sort"
)

// OverlayDiff 描述单个 key 在 base 与 over 之间的结构化差异。
// Kind 为 DiffKindAdded / DiffKindRemoved / DiffKindChanged 之一：
//   - added：key 仅存在于 over；
//   - removed：key 仅存在于 base；
//   - changed：两侧同名但值不同。
//
// 缺失一侧的 Base 或 Over 取零值空字符串；要区分"缺失"与"空字符串"应以
// Kind 为准，而不是以该侧取值是否为空为准。
type OverlayDiff struct {
	Key  string
	Base string
	Over string
	Kind string
}

// 差异种类，作为 OverlayDiff.Kind 的稳定取值。
const (
	DiffKindAdded   = "added"
	DiffKindRemoved = "removed"
	DiffKindChanged = "changed"
)

// noDiffSummary 是 SummarizeDiffs 在没有任何差异时返回的固定字面量。
const noDiffSummary = "无差异"

// DiagnoseOverlays 复用 DiffOverlays 给出的变化 key 集合，生成带两侧取值的结构化差异。
// 结果按 Key 的字典序稳定排序（CJK key 依 UTF-8 字节序），无变化时返回非 nil 的空切片。
// 本函数是纯函数，不修改任何入参，可从任意 goroutine 并发调用。
func DiagnoseOverlays(base, over Overlay) []OverlayDiff {
	changed := DiffOverlays(base, over)
	diffs := make([]OverlayDiff, 0, len(changed))
	for _, key := range changed {
		baseValue, inBase := base.Values[key]
		overValue, inOver := over.Values[key]
		switch {
		case !inBase && inOver:
			diffs = append(diffs, OverlayDiff{Key: key, Over: overValue, Kind: DiffKindAdded})
		case inBase && !inOver:
			diffs = append(diffs, OverlayDiff{Key: key, Base: baseValue, Kind: DiffKindRemoved})
		default:
			// 两侧同名；DiffOverlays 已确认二者取值不同。
			diffs = append(diffs, OverlayDiff{Key: key, Base: baseValue, Over: overValue, Kind: DiffKindChanged})
		}
	}
	// key 唯一，再按 Key 排一次可让排序保证不依赖 DiffOverlays 的实现细节。
	sort.Slice(diffs, func(i, j int) bool { return diffs[i].Key < diffs[j].Key })
	return diffs
}

// SummarizeDiffs 生成稳定的中文摘要：先给出总数，再分别统计新增、删除、变更的项数。
// 未知 Kind 仍计入总数，但不落入三类统计。
// diffs 为空（含 nil）时返回固定字面量 "无差异"。
// 本函数是纯函数，不修改入参，可从任意 goroutine 并发调用。
func SummarizeDiffs(diffs []OverlayDiff) string {
	if len(diffs) == 0 {
		return noDiffSummary
	}
	var added, removed, changed int
	for _, d := range diffs {
		switch d.Kind {
		case DiffKindAdded:
			added++
		case DiffKindRemoved:
			removed++
		case DiffKindChanged:
			changed++
		}
	}
	return fmt.Sprintf("共 %d 项变化：新增 %d 项，删除 %d 项，变更 %d 项", len(diffs), added, removed, changed)
}
