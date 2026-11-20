package session

// compactionThreshold 计算自动压缩的触发阈值：对比例阈值与绝对 token 上限取较小者，
// 即「先到者为准」，再用上下文窗口封顶。
//
// 参数语义：
//   - proportional：按比例算出的 token 阈值；仅正数值参与比较。
//   - absoluteCap：绝对 token 上限；仅正数值参与比较。
//   - contextWindow：模型上下文窗口；为正时作为最终结果的上限，非正时忽略该上限。
//
// 任一参数 <= 0 时忽略该项。若 proportional 与 absoluteCap 均非正，则返回 0，
// 表示缺少可用阈值；单独一个 contextWindow 不足以推导出触发点。
//
// 该函数只做整数比较，不涉及字节或符文统计，因此与 CJK 无关；结果恒为非负，
// 且不会超过任一参与比较的正数参数，是有界的。
func compactionThreshold(proportional, absoluteCap, contextWindow int) int {
	// 参与比较的候选值恒为正，因此用 0 表示「尚无候选」。
	threshold := 0
	if proportional > 0 {
		threshold = proportional
	}
	if absoluteCap > 0 && (threshold == 0 || absoluteCap < threshold) {
		threshold = absoluteCap
	}
	if threshold == 0 {
		return 0
	}
	if contextWindow > 0 && threshold > contextWindow {
		return contextWindow
	}
	return threshold
}
