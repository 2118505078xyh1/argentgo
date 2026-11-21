package tui

import "fmt"

// DisplayItem 是消息分组投影使用的轻量入参：它只携带分组与展示所
// 必需的最小信息，不依赖 Model，从而让分组逻辑保持为可独立测试的
// 纯函数。各字段含义：
//
//   - ID：消息在会话树中的稳定标识；
//   - Role：消息角色（user/assistant/tool 等），分组的唯一依据；
//   - Text：消息正文，折叠时会被替换为占位文案；
//   - Seq：单调递增序号，用于保持会话中的先后顺序。
type DisplayItem struct {
	ID   string
	Role string
	Text string
	Seq  uint64
}

// summaryRole 是折叠占位项固定的角色名。
const summaryRole = "summary"

// cloneItems 把 items 复制到一个全新的切片，确保返回值与入参不共享
// 底层数组；DisplayItem 全是值字段（字符串与 uint64），按值复制即可。
func cloneItems(items []DisplayItem) []DisplayItem {
	if len(items) == 0 {
		return nil
	}
	out := make([]DisplayItem, len(items))
	copy(out, items)
	return out
}

// GroupConsecutive 把连续且 Role 相同的项归入同一组，返回新的二维
// 结构。它是纯函数：不修改入参、不复用入参的底层数组，并严格保持
// 项与组的原有顺序。仅当相邻项角色变化时才切分新组；空输入返回 nil。
func GroupConsecutive(items []DisplayItem) [][]DisplayItem {
	if len(items) == 0 {
		return nil
	}
	groups := make([][]DisplayItem, 0)
	start := 0
	for i := 1; i < len(items); i++ {
		if items[i].Role != items[start].Role {
			groups = append(groups, cloneItems(items[start:i]))
			start = i
		}
	}
	groups = append(groups, cloneItems(items[start:]))
	return groups
}

// collapseGroup 把一个组折叠成单个占位项。占位项的 Role 固定为
// summary，Text 记录被折叠的项数与原角色；ID/Seq 取自组内首项，使
// 占位项仍带有可用于排序与定位的锚点（空组则为零值）。
func collapseGroup(group []DisplayItem) DisplayItem {
	role := ""
	item := DisplayItem{Role: summaryRole}
	if len(group) > 0 {
		role = group[0].Role
		item.ID = group[0].ID
		item.Seq = group[0].Seq
	}
	item.Text = fmt.Sprintf("collapsed %d %s messages", len(group), role)
	return item
}

// CollapseOldGroups 折叠较早的组，只保留最后 keepLast 组的全文。
//
//   - 前 len(groups)-keepLast 组各被替换为单个 Role="summary" 的占位
//     DisplayItem，其 Text 带有被折叠的项数；
//   - 最后 keepLast 组以全文形式保留（逐项复制到新切片）；
//   - keepLast < 0 按 0 处理，> len(groups) 按 len(groups) 处理；
//   - 空输入返回 nil。
//
// 返回值是新结构：既不修改入参，也不与入参共享底层数组，因此对返回
// 结果的后续改动不会回写到 groups。
func CollapseOldGroups(groups [][]DisplayItem, keepLast int) [][]DisplayItem {
	if len(groups) == 0 {
		return nil
	}
	if keepLast < 0 {
		keepLast = 0
	}
	if keepLast > len(groups) {
		keepLast = len(groups)
	}
	split := len(groups) - keepLast
	out := make([][]DisplayItem, 0, len(groups))
	for i, group := range groups {
		if i < split {
			out = append(out, []DisplayItem{collapseGroup(group)})
			continue
		}
		out = append(out, cloneItems(group))
	}
	return out
}
