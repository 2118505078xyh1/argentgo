package cli

import (
	"strconv"
)

// 上下文预算的接近上限阈值：估算用量达到上限的 9/10（即 90%）时给出警告。
// 以分子/分母常量而不是直接写 0.9，避免引入浮点运算带来的边界歧义。
const (
	contextBudgetWarnNumerator   = 9
	contextBudgetWarnDenominator = 10
)

// contextBudgetAdvice 根据「估算的上下文 token 用量」与「上下文上限」生成一条
// 简体中文的缩减建议，供发送前的上下文预检路径选择是否提示用户。
//
// 它是纯函数：只读两个整数参数，不读取配置、会话、tokenize 结果或全局状态，
// 也不产生任何 I/O，因此既能安全地用于表驱动测试，也能在将来由发送前预检
// 路径 opt-in 调用，而不改变当前任何命令的字节级输出（当前尚未接线，
// 见接线点报告）。CJK 宽字符的 token 折算由调用方用 tokenize.Estimate 先完成，
// 本函数只消费折算后的整数结果，不做任何文本或字节切分。
//
// 返回值约定：
//   - estimated <= 0 或 limit <= 0：信息不足，返回空串；
//   - estimated > limit（超限）：返回包含 /drop、/clear 与「拆小文件」的缩减建议；
//   - estimated 达到 limit 的 90%（接近上限）：返回一条提前缩减的警告；
//   - 其余（低于 90%）：返回空串。
//
// 返回文案仅由固定中文文本与两个十进制整数构成，长度有界，与 estimated、
// limit 的具体大小无关；不会把任何原文内容嵌入返回值。
func contextBudgetAdvice(estimated, limit int) string {
	if estimated <= 0 || limit <= 0 {
		return ""
	}

	prefix := "上下文预计用量约 " + strconv.Itoa(estimated) + " tokens"
	if estimated > limit {
		return prefix + "，已超过上限 " + strconv.Itoa(limit) +
			"；请用 /drop 移出不需要的文件、/clear 清空会话，或把大文件拆小后再发送"
	}

	// nearLimit 为向上取整后的 90% 阈值。先乘小的 (denominator-numerator)
	// 再除，且对 limit 只做一次不超过自身的乘法，避免 limit*numerator
	// 在大 limit 下溢出。
	nearLimit := limit - limit*(contextBudgetWarnDenominator-contextBudgetWarnNumerator)/contextBudgetWarnDenominator
	if estimated >= nearLimit {
		return prefix + "，已接近上限 " + strconv.Itoa(limit) +
			"；建议用 /drop 精简文件集，避免发送时超限"
	}
	return ""
}
