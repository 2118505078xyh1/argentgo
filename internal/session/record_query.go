package session

// record_query.go 提供对只读记录切片的纯查询函数。
//
// 这些函数只读入参，不修改 records 及其元素，也不修改 Data 的内容；
// 返回切片时一律分配新的底层数组。记录按传入顺序（即文件顺序）处理，
// 记录树由 id/parent 关系描述。

// FilterByKind 返回 kind 命中 kinds 中任一项的记录，保持原有顺序。
// kinds 为空时没有任何记录能命中，返回空切片；返回的切片与入参不共享底层数组。
func FilterByKind(records []Record, kinds ...string) []Record {
	out := make([]Record, 0, len(records))
	if len(kinds) == 0 {
		return out
	}
	wanted := make(map[string]struct{}, len(kinds))
	for _, kind := range kinds {
		wanted[kind] = struct{}{}
	}
	for _, record := range records {
		if _, ok := wanted[record.Kind]; ok {
			out = append(out, record)
		}
	}
	return out
}

// Ancestors 沿 parent 链向上收集 id 对应记录的所有祖先，不含自身。
// 结果从最近的父节点开始（链序）。当 parent 为空、parent 不在 records 中、
// 或再次遇到已访问 id（形成环）时停止，因此环不会导致死循环。
// id 不存在时返回空切片；返回的切片与入参不共享底层数组。
func Ancestors(records []Record, id string) []Record {
	byID := make(map[string]Record, len(records))
	for _, record := range records {
		byID[record.ID] = record
	}
	out := make([]Record, 0)
	current, ok := byID[id]
	if !ok {
		return out
	}
	seen := map[string]bool{id: true}
	for parentID := current.Parent; parentID != ""; {
		if seen[parentID] {
			break
		}
		parent, found := byID[parentID]
		if !found {
			break
		}
		out = append(out, parent)
		seen[parentID] = true
		parentID = parent.Parent
	}
	return out
}

// Descendants 返回 id 对应记录的所有后代，按它们在 records 中的出现顺序
// 稳定排列（与遍历顺序无关）。遍历使用 visited 集合防止环导致死循环，
// 且 id 自身永不进入结果，即使某条边指回它。
// id 不存在时返回空切片；返回的切片与入参不共享底层数组。
func Descendants(records []Record, id string) []Record {
	children := make(map[string][]string, len(records))
	exists := make(map[string]bool, len(records))
	for _, record := range records {
		exists[record.ID] = true
		if record.Parent != "" {
			children[record.Parent] = append(children[record.Parent], record.ID)
		}
	}
	if !exists[id] {
		return make([]Record, 0)
	}

	visited := map[string]bool{id: true}
	desc := make(map[string]bool)
	stack := append([]string(nil), children[id]...)
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if visited[cur] {
			continue
		}
		visited[cur] = true
		desc[cur] = true
		stack = append(stack, children[cur]...)
	}

	out := make([]Record, 0, len(desc))
	for _, record := range records {
		if desc[record.ID] {
			out = append(out, record)
		}
	}
	return out
}

// FirstMatch 返回 records 中第一个使 pred 为真的记录及存在标记。
// pred 为 nil 或没有命中时返回零值 Record 与 false；不修改入参。
func FirstMatch(records []Record, pred func(Record) bool) (Record, bool) {
	if pred == nil {
		return Record{}, false
	}
	for _, record := range records {
		if pred(record) {
			return record, true
		}
	}
	return Record{}, false
}
