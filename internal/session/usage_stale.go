package session

// isStaleUsage 判断一条用量记录是否已经陈旧，因而不能再作为当前的用量锚点。
// 陈旧的用量会在成本汇总与报告中失去参考价值：压缩会重置上下文，压缩前的计数
// 不再对应当前对话；错误回复通常没有真实计费，其用量同样不可信。
//
// 参数语义：
//   - usageTS：用量记录的时间戳。
//   - lastCompactionTS：最近一次压缩的时间戳；<= 0 表示尚无有效压缩（0 为哨兵）。
//   - isError：该用量是否来自错误回复。
//
// 判定规则：
//   - 错误回复的用量无条件视为陈旧，与时间戳无关。
//   - 没有有效压缩时间（<= 0）时不存在锚点，不因压缩而陈旧。
//   - 其余情况下，用量严格早于最近一次压缩（usageTS < lastCompactionTS）视为陈旧；
//     时间戳相等属于同一时刻，仍视为新鲜。
//
// 该函数只做整数比较，不涉及字节或符文统计，因此与 CJK 无关；它不读写任何状态，
// 结果只由三个入参决定，对相同输入恒定返回相同结果（稳定）。
func isStaleUsage(usageTS int64, lastCompactionTS int64, isError bool) bool {
	// 错误回复没有真实计费，其用量一律不可信。
	if isError {
		return true
	}
	// lastCompactionTS <= 0 是「从未压缩」的哨兵，没有可供比较的锚点。
	if lastCompactionTS <= 0 {
		return false
	}
	// 严格早于压缩的用量已经过时；时间戳相等属于同一时刻，仍然新鲜。
	return usageTS < lastCompactionTS
}
