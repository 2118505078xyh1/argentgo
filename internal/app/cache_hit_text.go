package app

import (
	"math"
	"strconv"
	"strings"
)

// 缓存统计文案的固定字面量。分母（提示 token 总计）不可用时整段文案标注为
// "缓存统计不可用"，不输出 NaN、Inf 或负 token 数，避免虚假数字被当成真实测量结果。
// 标签与标点均为简体中文常量，便于报告复用，也方便测试断言不依赖具体排版细节。
const (
	cacheHitUnavailableText = "缓存统计不可用"
	cacheHitHitLabel        = "缓存命中"
	cacheHitWriteLabel      = "写入"
	cacheHitTokensUnit      = "tokens"
)

// cacheHitText 生成一段中文缓存统计文案，展示缓存命中 token 数、命中百分比与
// 缓存写入 token 数。它是纯函数：只依赖三个入参，不读时钟、不访问全局状态，
// 也不产生副作用，因此对相同输入总是返回相同结果。
//
// 入参约定与钳制规则：
//   - total 为提示 token 总计（命中率的分母）。只有严格大于 0 才视为可用；
//     非正数（零值或负数，代表统计不可用）时整段文案退化为"缓存统计不可用"。
//   - read 为缓存命中（读取）token 数，负数按 0 处理，超过 total 按 total 截断。
//   - write 为缓存写入 token 数，负数按 0 处理，超过 total 按 total 截断。
//
// 命中百分比 = read / total * 100，四舍五入到整数并钳制到 0..100；由于 read
// 已裁剪到 0..total，百分比天然落在 0..100，绝不产生 NaN 或 Inf，也不会出现负号。
//
// 数值先转为 float64 再参与比值计算，全程只有除法与取整，没有整型乘法，
// 极端入参（如 math.MaxInt）也不会溢出。文案长度随入参位数线性增长但有明确上界，
// 不会产生无限长字符串。拼接过程不按字节截断，因此对 CJK 安全：
// 输出始终是合法 UTF-8，不会出现半个汉字或替换字符。
func cacheHitText(read, write, total int) string {
	if total <= 0 {
		return cacheHitUnavailableText
	}

	// 把命中与写入 token 裁剪到 0..total，杜绝负数与越界值泄漏到输出中。
	if read < 0 {
		read = 0
	} else if read > total {
		read = total
	}
	if write < 0 {
		write = 0
	} else if write > total {
		write = total
	}

	// 先转 float64 再做除法与百分比换算，规避整型溢出；命中率天然落在 0..100。
	pct := math.Round(float64(read) / float64(total) * 100)
	if pct < 0 {
		pct = 0
	} else if pct > 100 {
		pct = 100
	}

	var b strings.Builder
	b.WriteString(cacheHitHitLabel)
	b.WriteString(" ")
	b.WriteString(strconv.Itoa(read))
	b.WriteString(" ")
	b.WriteString(cacheHitTokensUnit)
	b.WriteString("（")
	b.WriteString(strconv.Itoa(int(pct)))
	b.WriteString("%")
	b.WriteString("）")
	b.WriteString("，")
	b.WriteString(cacheHitWriteLabel)
	b.WriteString(" ")
	b.WriteString(strconv.Itoa(write))
	return b.String()
}
