package agent

// effectiveCompactThreshold 计算最终生效的压缩阈值。
//
// 触发阈值同时受比例阈值与绝对上限约束：取两者中较小的正数值（先到者为准），
// 再用上下文窗口封顶，确保结果不会超过模型的可用窗口。
// 非正的参数表示"该项未设置"，会被忽略，不参与取最小值；
// 当三个参数全部非正时返回 0。
//
// 本函数只做整数比较，不处理任何文本或 rune，因此与 CJK 内容无关；
// 结果恒为传入正参数中的最小值，始终有界且不会发生溢出。
func effectiveCompactThreshold(proportional, absoluteCap, contextWindow int) int {
	threshold := 0
	if proportional > 0 {
		threshold = proportional
	}
	if absoluteCap > 0 && (threshold == 0 || absoluteCap < threshold) {
		threshold = absoluteCap
	}
	if contextWindow > 0 && (threshold == 0 || contextWindow < threshold) {
		threshold = contextWindow
	}
	return threshold
}
