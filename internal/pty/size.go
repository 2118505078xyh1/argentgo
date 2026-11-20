package pty

const (
	// 垃圾，结果都满足 Resize 的 >= minAxis 要求且能放进 uint16。
	minAxis = 2
	// minAxis 匹配 Start 和 Resize 已经接受的最小维度。maxAxis 是 Size 轴（uint16）能容纳的最大值。
	maxAxis = 65535
)

// FitSize 把请求的终端尺寸约束到可用范围。
//
// FitSize 把请求的终端尺寸约束到可用范围。cols 和 rows 是 UI/窗口 resize 事件的原始尺寸，它们是
// int，因为这些值常由像素测量推导或
// int，因为这样的值常由像素测量推导或从 JSON 解码，可能为零、负或荒谬地大。maxCols 和 maxRows 把结果限制在调用方能容纳的范围；非正数
// maxRows 把结果限制在调用方能容纳的范围；非正数
// 界限表示"无调用方限制"，只受 Size 能容纳的值约束。
//
// 界限表示"无调用方限制"，只受 Size 能容纳的值约束。传给 Resize 总是安全的：每轴至少 minAxis
// 且至多有效界限，且从不超过 uint16。minAxis 胜过较小的正最大值，使退化界限不会产生无用尺寸。FitSize 是纯函数且可并发安全使用。
// 较小的正最大值，使退化界限不会产生无用的
// 尺寸。FitSize 是纯函数且可并发安全使用。
func FitSize(cols, rows, maxCols, maxRows int) Size {
	return Size{
		Cols: fitAxis(cols, maxCols),
		Rows: fitAxis(rows, maxRows),
	}
}

// fitAxis 把单一维度限制到 [minAxis, 有效最大值]。
func fitAxis(value, max int) uint16 {
	if max <= 0 || max > maxAxis {
		max = maxAxis
	}
	if max < minAxis {
		max = minAxis
	}
	switch {
	case value < minAxis:
		return minAxis
	case value > max:
		return uint16(max)
	default:
		return uint16(value)
	}
}
