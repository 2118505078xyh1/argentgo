package session

import "strings"

// 本文件提供会话树的只读导出原语（R40 会话树可视化）：把 id/parent 记录投影为
// 带 children/深度/active 标记的节点切片，并把节点渲染成 Unicode 树形文本。
// 两者都是纯函数：不改入参、不访问文件系统、结果确定、遇到环不会无限递归。
// 本文件刻意不接线到 CLI/TUI，调用方按需使用。

// TreeNode 是会话树中的一个节点。
type TreeNode struct {
	ID       string
	Parent   string
	Children []string // 子节点 id，按记录在输入切片中的出现顺序（稳定）
	IsActive bool     // 该节点是否为当前活动叶节点
	Depth    int      // 距根的父跳数；根为 0
}

// ExportTree 把记录的 id/parent 关系构造成树节点切片，按深度优先前序稳定排列。
//
// 规则：
//   - 仅按 id/parent 建边；parent 为空或指向未知 id 的记录视为根。
//   - 兄弟顺序取记录在 records 中的出现顺序，因此输出确定、与 map 迭代顺序无关。
//   - 重复 id 只取首次出现，避免重复节点与重复子节点。
//   - activeID 为空时不标记任何节点；仅当节点 id 与 activeID 相等时 IsActive 为真。
//   - 遇到环（含自环）时用 visited 剪枝，保证每个节点恰好输出一次且不会无限递归；
//     无法从任何根到达的环节点按输入顺序补成额外的根，确保不丢节点。
//   - 不修改 records；返回的 Children 均为新分配的切片，不与内部状态共享。
func ExportTree(records []Record, activeID string) []TreeNode {
	// 以首次出现为准：既去重又保留输入顺序。
	order := make([]string, 0, len(records))
	byID := make(map[string]Record, len(records))
	for _, record := range records {
		if _, ok := byID[record.ID]; ok {
			continue
		}
		byID[record.ID] = record
		order = append(order, record.ID)
	}

	// 建子节点表；parent 不存在时该记录本身就是根，不挂到任何父节点下。
	children := make(map[string][]string, len(order))
	for _, id := range order {
		parent := byID[id].Parent
		if parent == "" {
			continue
		}
		if _, ok := byID[parent]; !ok {
			continue
		}
		children[parent] = append(children[parent], id)
	}

	out := make([]TreeNode, 0, len(order))
	visited := make(map[string]bool, len(order))
	var visit func(id string, depth int)
	visit = func(id string, depth int) {
		if visited[id] {
			return
		}
		visited[id] = true
		record := byID[id]
		kids := children[id]
		childIDs := make([]string, len(kids))
		copy(childIDs, kids)
		out = append(out, TreeNode{
			ID:       id,
			Parent:   record.Parent,
			Children: childIDs,
			IsActive: activeID != "" && id == activeID,
			Depth:    depth,
		})
		for _, child := range kids {
			visit(child, depth+1)
		}
	}

	// 先从根出发做深度优先前序遍历。
	for _, id := range order {
		parent := byID[id].Parent
		if parent == "" {
			visit(id, 0)
			continue
		}
		if _, ok := byID[parent]; !ok {
			visit(id, 0)
		}
	}
	// 纯环中的节点没有根：把尚未访问的节点按输入顺序补成额外的根，保证不丢节点。
	for _, id := range order {
		if !visited[id] {
			visit(id, 0)
		}
	}
	return out
}

const (
	asciiBranch   = "├── "
	asciiLast     = "└── "
	asciiVertical = "│   "
	asciiBlank    = "    "
	asciiActive   = "  ← active"
)

// RenderTreeASCII 把节点渲染成 Unicode 树形文本，行以 "\n" 连接，末尾不带换行。
//
// 层级由 Parent/Children 重建，不依赖 nodes 的排列顺序，兄弟顺序取 Children。
// 前缀只使用 ASCII 与制表线字符，节点标签按字节原样拼接、从不按字节切片，
// 因此 CJK 等多字节标题不会被截断成半个 UTF-8，也不会因字节宽度而错位；
// active 节点的行尾追加 "  ← active" 标记。
// 对环与未知 id 安全：已访问节点与不存在的子节点会被跳过；空输入返回空串。
func RenderTreeASCII(nodes []TreeNode) string {
	if len(nodes) == 0 {
		return ""
	}
	byID := make(map[string]TreeNode, len(nodes))
	for _, node := range nodes {
		if _, ok := byID[node.ID]; !ok {
			byID[node.ID] = node
		}
	}

	var lines []string
	visited := make(map[string]bool, len(byID))
	// connector 为空表示根节点（无连接线、无缩进）。
	var emit func(id, prefix, connector string)
	emit = func(id, prefix, connector string) {
		if visited[id] {
			return
		}
		visited[id] = true
		node := byID[id]
		label := node.ID
		if node.IsActive {
			label += asciiActive
		}
		lines = append(lines, prefix+connector+label)

		childPrefix := prefix
		if connector != "" {
			if connector == asciiLast {
				childPrefix += asciiBlank
			} else {
				childPrefix += asciiVertical
			}
		}
		// 只渲染确实存在的子节点；最后一个有效子节点用 └ 连接线。
		valid := make([]string, 0, len(node.Children))
		for _, child := range node.Children {
			if _, ok := byID[child]; ok {
				valid = append(valid, child)
			}
		}
		for i, child := range valid {
			next := asciiBranch
			if i == len(valid)-1 {
				next = asciiLast
			}
			emit(child, childPrefix, next)
		}
	}

	// 根：Parent 为空或指向未知 id 的节点，按 nodes 出现顺序渲染。
	for _, node := range nodes {
		parent := node.Parent
		if parent == "" {
			emit(node.ID, "", "")
			continue
		}
		if _, ok := byID[parent]; !ok {
			emit(node.ID, "", "")
		}
	}
	// 纯环中的节点没有根：把尚未访问的节点按输入顺序补成额外的根。
	for _, node := range nodes {
		if !visited[node.ID] {
			emit(node.ID, "", "")
		}
	}
	return strings.Join(lines, "\n")
}
