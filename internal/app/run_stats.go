package app

import (
	"math"
	"strconv"
	"strings"
)

// 单次运行统计文案的固定字面量。所有无法数值化的指标统一标注为"不可用"，
// 不输出 NaN、Inf 或负耗时，避免虚假数字被当成真实测量结果。
const (
	runStatsUnavailable     = "不可用"
	runStatsTTFTLabel       = "首 token 延迟"
	runStatsTotalLabel      = "总耗时"
	runStatsThroughputLabel = "吞吐"
	runStatsTokensPerSec    = "tokens/s"
	runStatsSeparator       = " · "
)

// runStatsText 生成一段中文单次运行统计文案，覆盖首 token 延迟（TTFT）、
// 总耗时与输出吞吐（tokens/sec）。它是纯函数：只依赖三个入参，不读时钟、
// 不访问全局状态，也不产生副作用，因此对相同输入总是返回相同结果。
//
// 入参约定与钳制规则：
//   - firstTokenMS 为首 token 延迟（毫秒）。只有严格大于 0 才视为有效测量；
//     负数与零被钳制为不可用。
//   - totalMS 为整次运行耗时（毫秒）。同样只有严格大于 0 才有效。
//   - tokens 为生成的 token 数。只有严格大于 0 才有效。
//
// 吞吐的计算窗口优先取生成阶段耗时（totalMS - firstTokenMS），因为 TTFT 之后
// 才是真正的解码阶段；当 TTFT 不可用或出现倒置（firstTokenMS >= totalMS）时，
// 退化为用 totalMS 作为窗口，给出整次运行的平均速率。缺少 tokens 或任何可用
// 时间窗口时，吞吐标注为不可用。
//
// 所有数值都先转为 float64 再参与运算，避免 int64/int 相乘溢出；结果只做有界
// 格式化（固定小数位），文案长度随入参位数线性增长但有明确上界，不会产生
// 无限长字符串。标签为简体中文常量，拼接过程不按字节截断，因此对 CJK 安全：
// 输出始终是合法 UTF-8，不会出现半个汉字或替换字符。
func runStatsText(firstTokenMS int64, totalMS int64, tokens int) string {
	ttft := runStatsField(runStatsTTFTLabel, firstTokenMS > 0, runStatsDuration(firstTokenMS))
	total := runStatsField(runStatsTotalLabel, totalMS > 0, runStatsDuration(totalMS))

	throughputOK := tokens > 0 && totalMS > 0
	throughputValue := ""
	if throughputOK {
		windowMS := totalMS
		if firstTokenMS > 0 && firstTokenMS < totalMS {
			windowMS = totalMS - firstTokenMS
		}
		// 先转 float64 再乘 1000，避免大 token 数与毫秒相乘时在整型里溢出。
		rate := float64(tokens) * 1000 / float64(windowMS)
		if math.IsInf(rate, 0) || math.IsNaN(rate) {
			throughputOK = false
		} else {
			// 把单位并入数值，使可用分支渲染为"吞吐 55.6 tokens/s"。
			throughputValue = strconv.FormatFloat(rate, 'f', 1, 64) + " " + runStatsTokensPerSec
		}
	}
	throughput := runStatsField(runStatsThroughputLabel, throughputOK, throughputValue)

	var b strings.Builder
	b.WriteString(ttft)
	b.WriteString(runStatsSeparator)
	b.WriteString(total)
	b.WriteString(runStatsSeparator)
	b.WriteString(throughput)
	return b.String()
}

// runStatsField 把单个指标渲染为"标签 数值"或"标签不可用"。
// 可用状态下数值仍必须非空，否则按不可用处理，杜绝空白指标泄漏到输出中。
func runStatsField(label string, ok bool, value string) string {
	if !ok || value == "" {
		return label + runStatsUnavailable
	}
	return label + " " + value
}

// runStatsDuration 把毫秒数格式化为有界的可读时长：不足一秒用毫秒整数，
// 否则用秒并保留两位小数。调用方保证传入非负数，负值与零由上方钳制为不可用。
func runStatsDuration(ms int64) string {
	if ms < 1000 {
		return strconv.FormatInt(ms, 10) + "ms"
	}
	return strconv.FormatFloat(float64(ms)/1000, 'f', 2, 64) + "s"
}
