package provider

import "sort"

// Overlay 描述分层配置中的一层覆盖：Name 是参与排序的稳定标识，
// Priority 决定应用顺序（数值越大越晚应用），Values 保存该层的 key/value。
// 它只描述数据，不含合并策略；合并策略由 MergeOverlays 实现。
type Overlay struct {
	Name     string
	Priority int
	Values   map[string]string
}

// MergeOverlays 按 (Priority, Name) 升序依次应用各层，后应用的层覆盖同名 key，
// 返回一个与所有入参完全隔离的新 map。
//
// 排序是稳定的：Priority 与 Name 都相同时保持它们在 overlays 中的先后顺序，
// 因此调用方应保证 (Priority, Name) 组合唯一，才能获得与输入排列无关的结果。
// 空或 nil 切片返回非 nil 的空 map，便于调用方直接写入。
// 本函数是纯函数，不修改 overlays 切片及其 Values，可从任意 goroutine 并发调用。
func MergeOverlays(overlays []Overlay) map[string]string {
	out := make(map[string]string)
	if len(overlays) == 0 {
		return out
	}

	// 先复制切片再排序，避免重排调用方的切片。
	ordered := make([]Overlay, len(overlays))
	copy(ordered, overlays)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Priority != ordered[j].Priority {
			return ordered[i].Priority < ordered[j].Priority
		}
		return ordered[i].Name < ordered[j].Name
	})

	for _, layer := range ordered {
		for key, value := range layer.Values {
			out[key] = value
		}
	}
	return out
}

// DiffOverlays 返回 base 与 over 之间发生变化的 key：任一侧新增、删除，
// 或两侧同名但值不同都算变化（空字符串与缺失也视为不同）。
// 结果按 key 的字典序稳定排序（CJK key 依 UTF-8 字节序），无变化时返回空切片。
// 本函数不修改任何入参，可从任意 goroutine 并发调用。
func DiffOverlays(base, over Overlay) []string {
	keys := make(map[string]struct{}, len(base.Values)+len(over.Values))
	for key := range base.Values {
		keys[key] = struct{}{}
	}
	for key := range over.Values {
		keys[key] = struct{}{}
	}

	changed := make([]string, 0, len(keys))
	for key := range keys {
		baseValue, inBase := base.Values[key]
		overValue, inOver := over.Values[key]
		if inBase != inOver || baseValue != overValue {
			changed = append(changed, key)
		}
	}
	sort.Strings(changed)
	return changed
}
