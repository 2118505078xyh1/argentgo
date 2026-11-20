package agent

import "strconv"

// budgetTierPercents 是上下文窗口占用率的里程碑，达到时模型应收到一条模型可见的提醒。
// 层级编号是已跨越的里程碑数量，因此会随着窗口被填满而单调增长。
var budgetTierPercents = [4]int{50, 75, 90, 95}

// BudgetTier 返回 used/window 已跨越的占用率里程碑数量：
// 低于 50% 为 0，达到 50%/75%/90%/95% 时依次为 1/2/3/4。比较使用
// 整数运算（used*100 >= percent*window），这样恰好占用一半窗口这类边界
// 会归入较高的层级，而不会出现浮点误差。
//
// 非正输入作为占用率没有意义，返回 0，因此调用方可以直接传入原始的用量/提供方数值，
// 无需先做清洗。token 数量远低于 int64 范围，扩宽后的乘积实际上不会溢出；
// used >= window 的短路也覆盖了窗口被完全耗尽的情形。
func BudgetTier(used, window int) int {
	if used <= 0 || window <= 0 {
		return 0
	}
	if used >= window {
		return len(budgetTierPercents)
	}
	tier := 0
	for _, percent := range budgetTierPercents {
		if int64(used)*100 < int64(percent)*int64(window) {
			break
		}
		tier++
	}
	return tier
}

// ShouldRemind 决定是否要为 curTier 发送预算提醒，并把决定记录在 delivered 中，
// 使同一层级在同一个上下文窗口内不会重复播报。它是纯决策层：无 I/O、无时钟、不构造事件，
// 由调用方决定如何（或是否）把 true 结果呈现出来。
//
// 当占用层级上升（curTier > prevTier）或开启新窗口时需要提醒。
// 层级持平或下降时保持静默。delivered 的键是层级号；newWindow 会先清空它们，
// 使相同层级能在新窗口中再次告警。传入 nil 的 delivered 是安全的：
// 仍会返回决定，但不会记录任何内容。
func ShouldRemind(prevTier, curTier int, delivered map[int]bool, newWindow bool) bool {
	if newWindow {
		for tier := range delivered {
			delete(delivered, tier)
		}
	} else if curTier <= prevTier {
		return false
	}
	if delivered[curTier] {
		return false
	}
	if delivered != nil {
		delivered[curTier] = true
	}
	return true
}

// BudgetReminderText 生成一条模型可见的英文预算提醒。它刻意与地区无关：
// 这条指令是写进提示流供模型阅读的，而不是展示给操作人员。
//
// remaining 在格式化前会被收紧到零，并以 "k" 为单位呈现
// （1000 -> "1k"，1234 -> "1.2k"）；确切数字只是近似值，因此使用了
// "approximately"（大约）的措辞。window 参数是为了与 BudgetTier 对称以及便于日后调整措辞而保留，
// 但消息报告的是绝对剩余数量，有意与它无关。该函数纯净且稳定：
// 相同的参数总是产生相同的字符串。
func BudgetReminderText(remaining, window int) string {
	_ = window
	return "You have approximately " + formatTokensK(remaining) +
		" tokens left in this context window; consider wrapping up or summarizing."
}

// formatTokensK 以千为单位呈现 token 数，四舍五入到 0.1k 并省略末尾的 ".0"。
// 负数会变成 "0k"。
func formatTokensK(n int) string {
	if n < 0 {
		n = 0
	}
	k := n / 1000
	tenth := (n%1000 + 50) / 100 // 0..10，四舍五入到最接近的 100 个 token
	if tenth >= 10 {
		k++
		tenth = 0
	}
	if tenth == 0 {
		return strconv.Itoa(k) + "k"
	}
	return strconv.Itoa(k) + "." + strconv.Itoa(tenth) + "k"
}
