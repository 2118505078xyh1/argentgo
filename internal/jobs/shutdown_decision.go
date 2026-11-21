package jobs

// 退出决策的返回值。它们是稳定的 ASCII 字符串，供退出状态机据此选择下一步动作。
const (
	shutdownNone     = "none"
	shutdownGraceful = "graceful"
	shutdownForce    = "force"
)

// shutdownDecision 根据已收到的退出信号次数与宽限期是否结束，决定退出流程
// 下一步应采用的动作。它把「首信号给宽限、二次信号或宽限耗尽即强杀」的
// 状态机判定收敛为一个可在任意层复用的纯函数。
//
// 决策表：
//   - signalCount <= 0（无信号）→ "none"，宽限标志此时无关；
//   - signalCount >= 2（收到二次信号）→ "force"；
//   - signalCount == 1 且 graceElapsed 为真（宽限耗尽且无二次信号）→ "force"；
//   - signalCount == 1 且 graceElapsed 为假（首信号、宽限未到）→ "graceful"。
//
// 这是纯函数：不修改入参、不读取全局状态、不依赖时间或 locale，返回值只由
// 入参决定，因此对同一入参恒定（稳定）。入参只有整数与布尔值，返回固定的
// ASCII 字符串，因而与 CJK 等任何字符编码无关；负数 signalCount 按无信号处理。
func shutdownDecision(signalCount int, graceElapsed bool) string {
	if signalCount <= 0 {
		return shutdownNone
	}
	if signalCount >= 2 {
		return shutdownForce
	}
	// 至此 signalCount 必为 1，只需看宽限期是否已结束。
	if graceElapsed {
		return shutdownForce
	}
	return shutdownGraceful
}
