package tui

// viewportSlice 按视口行数从 entries 中切出可见行：从 start 开始，
// 最多返回 height 行，不足则到末尾。它是「先把长列表按视口行数切片、
// 再只渲染可见行」这一优化的基础原语，避免渲染全部内容后再切掉。
//
// 边界语义：
//   - start 为负时钳到 0；
//   - start 越界（start >= len(entries)）或 height <= 0 时返回 nil；
//   - 可见行不足 height 时只返回剩余的行。
//
// 它按元素（每个元素代表一行）而非字节切片，因此汉字、emoji 与其他
// 多字节 rune 始终完整（CJK/字素安全），且从不触碰单个元素的内容。
// 返回的是一个独立的新切片：调用方改动它不会污染输入；相同输入始终
// 得到相同顺序与内容的元素（稳定）。
func viewportSlice(entries []string, start, height int) []string {
	if height <= 0 || len(entries) == 0 {
		return nil
	}
	if start < 0 {
		start = 0
	}
	if start >= len(entries) {
		return nil
	}
	end := start + height
	// height 极大时 start+height 可能溢出为负数，这里一并钳到末尾，
	// 保证区间始终落在 [0, len(entries)] 之内。
	if end < start || end > len(entries) {
		end = len(entries)
	}
	return append([]string(nil), entries[start:end]...)
}
