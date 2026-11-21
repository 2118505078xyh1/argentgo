package agent

import "math"

// 预测下一步用量时考虑的最近样本数量。窗口过长会让早期用量稀释最新趋势，
// 取一个小窗口使外推更贴近"下一步"这一预测目标。
const forecastWindow = 4

// TokenTrend 是一次运行最近的每轮 token 用量样本，按时间先后排列，
// 最后一个元素最新。它只承载数值，不持有状态，也不修改调用方传入的切片。
type TokenTrend struct {
	Samples []int
}

// ForecastNextTokens 用最近若干轮的用量线性外推下一轮用量。
//
// 实现要点：
//   - 少于两个样本时退化为"最后一个样本"，没有样本返回 0；
//   - 负样本按 0 处理，结果钳制为非负，因此不会出现 NaN 或负数；
//   - 基线取窗口内样本的移动平均，斜率取首尾差除以步数（四舍五入到最近整数），
//     预测值为"平均值 + 斜率"，即在近期水平上再外推一步；
//   - 所有中间运算用 int64 并做饱和加法，极端输入（math.MaxInt/math.MinInt）
//     不会溢出或回绕成负数。
//
// 该函数是纯函数：不读取外部状态、不修改入参，输出只由入参决定。
func ForecastNextTokens(usageHistory []int) int {
	if len(usageHistory) == 0 {
		return 0
	}
	if len(usageHistory) < 2 {
		return clampToInt(nonNeg(int64(usageHistory[len(usageHistory)-1])))
	}

	start := len(usageHistory) - forecastWindow
	if start < 0 {
		start = 0
	}
	window := usageHistory[start:]

	var sum int64
	for _, v := range window {
		sum = satAdd(sum, nonNeg(int64(v)))
	}
	count := int64(len(window))
	avg := sum / count

	first := nonNeg(int64(window[0]))
	last := nonNeg(int64(window[len(window)-1]))
	slope := divRound(last-first, count-1)

	return clampToInt(nonNeg(satAdd(avg, slope)))
}

// WillExceedBudget 报告"已用 used 再花 forecast"是否会达到或超过预算。
// budget<=0 表示不设限，返回 false。两个用量都按非负处理，相加用饱和加法，
// 因此极大输入不会回绕成负数而误判为未超出。达到边界（相等）即视为会超出，
// 与 Budget.Breach 的 >= 语义一致。
func WillExceedBudget(forecast, used, budget int) bool {
	if budget <= 0 {
		return false
	}
	total := satAdd(nonNeg(int64(used)), nonNeg(int64(forecast)))
	return total >= int64(budget)
}

// SuggestTrimTokens 建议为把总用量压回预算所需裁剪的 token 数，
// 即 used+forecast 超出 budget 的部分；未超出时返回 0。budget<=0（不设限）
// 同样返回 0。先减去预算再加预测值，避免先相加再相减在大数饱和时丢失超出量。
func SuggestTrimTokens(used, forecast, budget int) int {
	if budget <= 0 {
		return 0
	}
	excess := nonNeg(int64(used)) - int64(budget)
	excess = satAdd(excess, nonNeg(int64(forecast)))
	if excess <= 0 {
		return 0
	}
	return clampToInt(excess)
}

// nonNeg 把负数钳为 0。
func nonNeg(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}

// satAdd 对 int64 做饱和加法，正/负溢出分别停在 MaxInt64/MinInt64。
func satAdd(a, b int64) int64 {
	if b > 0 && a > math.MaxInt64-b {
		return math.MaxInt64
	}
	if b < 0 && a < math.MinInt64-b {
		return math.MinInt64
	}
	return a + b
}

// divRound 计算 numer/denom 并四舍五入到最近整数（半数远离零）。
// denom 必须为正；饱和处理分子加半数时的正向溢出。
func divRound(numer, denom int64) int64 {
	if denom <= 0 {
		return 0
	}
	if numer >= 0 {
		return satAdd(numer, denom/2) / denom
	}
	return -(satAdd(-numer, denom/2) / denom)
}

// clampToInt 把 int64 钳制到 int 的可表示范围。
func clampToInt(v int64) int {
	if v > math.MaxInt {
		return math.MaxInt
	}
	if v < math.MinInt {
		return math.MinInt
	}
	return int(v)
}
