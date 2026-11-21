package tui

import (
	"sort"
	"strings"
)

// ApprovalView 是审批队列里一次待确认调用的轻量只读投影：它只携带 dock
// 展示所需的最小信息，不依赖 Model，也不引用 security 的任何类型，从而
// 让排序与单行文案的生成保持为可独立测试的纯函数。各字段含义：
//
//   - Name / Args：工具名与参数摘要（Args 可能是 JSON 文本）；
//   - Reason：调用被标记时的原因（danger 原因或拒绝反馈）；
//   - Danger：是否需要醒目的危险标记；
//   - Seq：单调递增序号，用于恢复审批项原本的先后顺序。
//
// 本类型目前没有任何调用方；默认零值完全惰性，对界面无任何影响。
type ApprovalView struct {
	Name   string
	Args   string
	Reason string
	Danger bool
	Seq    uint64
}

// ApprovalLine 使用的稳定文案常量。集中为常量，日后可整批迁往 localeStrings，
// 无需改动 ProjectApprovals / ApprovalLine 的控制流；与 turn_projection.go 的
// 阶段文案、feedback_state.go 的提示文案保持同样的"稳定文案"约定。
const (
	approvalCursorSelected = "> "  // 选中项的光标前缀
	approvalCursorIdle     = "  "  // 未选中项的等宽占位，保证各行列对齐
	approvalDangerMark     = "⚠ "  // 危险项标记
	approvalReasonSep      = " — " // 原因与主体之间的分隔符

	// approvalMaxReasonCells 限制单行里 reason 的显示宽度（按单元格计，
	// CJK 汉字与 emoji 各占两格）。40 与工具参数摘要既有的截断宽度同量级。
	approvalMaxReasonCells = 40
)

// ProjectApprovals 把审批项投影为 dock 列表顺序：先按 Seq 升序稳定排序，
// 再把 selected 指向的那一项提升到首位（dock 聚焦），其余项保持排序后的
// 相对顺序。
//
// selected 是入参 items 中的下标（调用方的选中位置）。排序会改变顺序，
// 因此提升时按下标锁定身份，而不是按值匹配——即使两项内容完全相同，
// 也只有 selected 指定的那一项会被提升。selected < 0 或 >= len(items) 时
// 不做提升，仅返回排序结果。
//
// 纯函数：返回全新切片（不与入参共享底层数组），不修改 items；空输入返回 nil。
func ProjectApprovals(items []ApprovalView, selected int) []ApprovalView {
	if len(items) == 0 {
		return nil
	}
	// 携带原始下标参与排序，使提升能精确定位 selected 项，
	// 且 Seq 相同的项由稳定排序保持原有先后顺序。
	type entry struct {
		view ApprovalView
		idx  int
	}
	entries := make([]entry, len(items))
	for i, v := range items {
		entries[i] = entry{view: v, idx: i}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].view.Seq < entries[j].view.Seq
	})
	if selected >= 0 && selected < len(entries) {
		for i := range entries {
			if entries[i].idx != selected {
				continue
			}
			focused := entries[i]
			copy(entries[1:i+1], entries[:i])
			entries[0] = focused
			break
		}
	}
	out := make([]ApprovalView, len(entries))
	for i := range entries {
		out[i] = entries[i].view
	}
	return out
}

// ApprovalLine 把一个审批项渲染成稳定的单行文本，供 dock 列表逐行显示。
// 格式如下（各标记的取值见上方常量）：
//
//	<光标><危险标记><名称> <参数> — <截断后的原因>
//
// 其中 Name / Args / Reason 都先经 collapseSpace 折叠所有空白（含换行），
// 保证结果始终是单行；Reason 再用 Truncate 按显示单元格有界截断，切口
// 落在字素边界，因此 CJK 汉字与 emoji 不会被切坏。selected 只影响光标
// 前缀，不改其余内容；Danger 只决定是否附加危险标记。
//
// 纯函数：不修改 v、不读取时钟或 Model，同一输入永远得到同一输出。
func ApprovalLine(v ApprovalView, selected bool) string {
	cursor := approvalCursorIdle
	if selected {
		cursor = approvalCursorSelected
	}
	var b strings.Builder
	b.WriteString(cursor)
	if v.Danger {
		b.WriteString(approvalDangerMark)
	}
	b.WriteString(collapseSpace(v.Name))
	if args := collapseSpace(v.Args); args != "" {
		b.WriteString(" ")
		b.WriteString(args)
	}
	if reason := collapseSpace(v.Reason); reason != "" {
		b.WriteString(approvalReasonSep)
		b.WriteString(Truncate(reason, approvalMaxReasonCells))
	}
	return b.String()
}
