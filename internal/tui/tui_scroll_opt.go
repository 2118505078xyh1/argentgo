package tui

// 本文件刻意自包含：它把记录滚动算术提取为
// 可在没有 Model、Buffer 或终端的情况下测试的纯辅助函数。
// 把它们接入 model_mutate.go / render.go 留给
// 这些文件的负责人。
//
// 记录滚动偏移是相对底部的：0 表示"最新行
// 可见"，较大的值表示"向前滚动更远"。render.go 从
// len(body)-m.scroll 推导可见行范围，因此大于
// len(body)-bodyH 的值没有视觉效果，但仍必须由后续的
// PageDown 按键撤销。在输入时钳制两端，使大型会话
// 在反复滚轮/翻页后不会感觉卡在顶部。

const (
	maxScrollInt = int(^uint(0) >> 1)
	minScrollInt = -maxScrollInt - 1
)

// saturatingScrollAdd 把两个偏移相加而不在极端
// 输入下回绕，使调用方之后能安全地钳制结果。
func saturatingScrollAdd(a, b int) int {
	if b > 0 && a > maxScrollInt-b {
		return maxScrollInt
	}
	if b < 0 && a < minScrollInt-b {
		return minScrollInt
	}
	return a + b
}

// transcriptScrollMax 是对共 totalLines 行、在 viewportHeight 行视口
// 中显示的记录的最大有意义的相对底部偏移。
// 放得下的记录，或非正的 total/viewport，根本没有
// 可滚动范围。
func transcriptScrollMax(totalLines, viewportHeight int) int {
	if totalLines <= 0 || viewportHeight <= 0 {
		return 0
	}
	max := totalLines - viewportHeight
	if max < 0 {
		return 0
	}
	return max
}

// clampTranscriptScroll 把可能陈旧的偏移带回
// [0, transcriptScrollMax]，例如在记录缩短或终端
// 变高之后。
func clampTranscriptScroll(offset, totalLines, viewportHeight int) int {
	max := transcriptScrollMax(totalLines, viewportHeight)
	if offset <= 0 {
		return 0
	}
	if offset > max {
		return max
	}
	return offset
}

// scrollTranscript 应用一次滚动步骤并在同一操作中钳制。
// 正的 delta 向更旧的输出方向滚动，负的 delta 向
// 最新行方向滚动。在这里钳制（而非仅在绘制时）
// 防止过度滚动累积：即使已经越过最旧行几十次滚轮，一次
// PageDown 仍撤销一次 PageUp。
func scrollTranscript(offset, delta, totalLines, viewportHeight int) int {
	return clampTranscriptScroll(saturatingScrollAdd(offset, delta), totalLines, viewportHeight)
}

// transcriptScrollAfterAppend 返回新行到达后的偏移。当
// pinnedToBottom 为 true 时视图跟随流并保持在 0。
// 否则相对底部的偏移增加 appendedLines，使相同的
// 绝对行留在屏幕上，然后钳制到新边界。
// 非正的 appendedLines 只是重新钳制当前偏移。
func transcriptScrollAfterAppend(offset, appendedLines, totalLines, viewportHeight int, pinnedToBottom bool) int {
	if pinnedToBottom {
		return 0
	}
	if appendedLines > 0 {
		return scrollTranscript(offset, appendedLines, totalLines, viewportHeight)
	}
	return clampTranscriptScroll(offset, totalLines, viewportHeight)
}
