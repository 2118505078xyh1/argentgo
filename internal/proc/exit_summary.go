package proc

// 批量执行状态汇总使用的稳定标签。它们是纯 ASCII 字符串，不随语言环境变化；
// 上层据此聚合或展示，无需各自重复判断退出码与超时标记。
const (
	exitSummarySuccess = "success"
	exitSummaryFailure = "failure"
	exitSummaryTimeout = "timeout"
)

// summarizeExits 把一次多进程批量执行的退出码与超时标记汇总为计数字典。
//
// codes 与 timeouts 按索引一一对应：第 i 个子进程的退出码是 codes[i]，
// 它是否超时由 timeouts[i] 标记。判定顺序明确且互不重叠：
//
//  1. i < len(timeouts) 且 timeouts[i] 为 true → "timeout"。超时标记是对
//     结局最直接的说明，优先于退出码；否则超时进程可能带着 0 或其他退出码，
//     单看 code 会被误判为成功或失败。
//  2. codes[i] == 0 → "success"，进程正常结束。
//  3. 其余任何非零（含负数）退出码 → "failure"，通用失败。
//
// 长度不一致时以 codes 为准：只遍历 codes 的元素；timeouts 较短时缺失的
// 标记按 false 处理，较长时多出的标记被忽略。
//
// 返回的 map 始终包含 success / failure / timeout 三个键（计数可为 0），
// 因此结果形状稳定，序列化后的键集合也保持一致。
//
// 这是纯函数：不读取任何全局状态，不对输入做文本解码，因而与 CJK / 多字节
// 编码无关（退出码只是整数，超时标记只是布尔值，不触碰任何字符串）；相同
// 输入永远得到相同输出，可被并发调用而无需同步。
func summarizeExits(codes []int, timeouts []bool) map[string]int {
	summary := map[string]int{
		exitSummarySuccess: 0,
		exitSummaryFailure: 0,
		exitSummaryTimeout: 0,
	}
	for i, code := range codes {
		switch {
		case i < len(timeouts) && timeouts[i]:
			summary[exitSummaryTimeout]++
		case code == 0:
			summary[exitSummarySuccess]++
		default:
			summary[exitSummaryFailure]++
		}
	}
	return summary
}
