package app

import (
	"math"
	"strconv"

	"example.com/argentgo/internal/llm"
)

// CacheVisibility 是一次回复的提示缓存统计快照，供报告与诊断展示。
// 全部字段都是纯数值，不含任何字符串或提供方信息，因此与语言环境无关，
// 也不涉及 CJK 字节处理。
type CacheVisibility struct {
	PromptTokens int // 提示 token 总计（新增 + 缓存）
	CachedTokens int // PromptTokens 中由提示缓存提供的部分
	FreshTokens  int // 未由缓存提供的部分（口径来自 llm.Usage.FreshPromptTokens）
	HitPercent   int // 命中百分比，钳制在 0..100
}

// CacheHint 系列提示使用的固定中文字面量。它们都是常量字节序列，
// 便于报告复用，也方便测试断言不依赖具体排版细节。
const (
	cacheHintEnabled          = "缓存已启用"
	cacheHintLocal            = "本地端点不支持缓存"
	cacheHintOpenAINeedOptIn  = "OpenAI 兼容端点需显式开启缓存"
	cacheHintNotEnabled       = "该端点未启用提示缓存"
	cacheHintUnknown          = "该客户端未声明缓存支持"
	cacheHintStatsUnavailable = "prompt cache 统计不可用"
)

// cacheHitPercent 计算命中百分比并钳制到 0..100。判定顺序确定：
//   - cached <= 0（无命中）或 prompt <= 0（分母不可用）时为 0；
//   - cached >= prompt（全命中或越界）时为 100；
//   - 其余情形按 cached/prompt*100 四舍五入到整数。
//
// 比值先转 float64 再计算，规避整型乘法溢出；四舍五入后再钳制一次，
// 保证极端入参也绝不产生 NaN、Inf 或越界值。纯整数运算，与 CJK 无关。
func cacheHitPercent(prompt, cached int) int {
	if cached <= 0 || prompt <= 0 {
		return 0
	}
	if cached >= prompt {
		return 100
	}
	pct := math.Round(float64(cached) / float64(prompt) * 100)
	if pct < 0 {
		return 0
	}
	if pct > 100 {
		return 100
	}
	return int(pct)
}

// CacheStats 把 llm.Usage 折算成一份可展示的缓存统计快照。
// 它是纯函数：u 按值传入且只含整数，不修改入参、不读时钟、不访问全局状态，
// 因此对相同入参总是返回逐字节相同的结果。
//
// FreshTokens 直接复用 llm.Usage.FreshPromptTokens 的口径，
// 确保与上游对"未由缓存提供的提示 token"的定义一致；
// HitPercent 按 cacheHitPercent 的规则钳制，不触碰 loop_helpers 的用量累加。
func CacheStats(u llm.Usage) CacheVisibility {
	return CacheVisibility{
		PromptTokens: u.PromptTokens,
		CachedTokens: u.CachedTokens,
		FreshTokens:  u.FreshPromptTokens(),
		HitPercent:   cacheHitPercent(u.PromptTokens, u.CachedTokens),
	}
}

// CacheHintCapability 基于 llm.CacheSupport(c) 返回一句稳定的中文能力提示。
// 它是纯函数：不修改入参、不发送任何请求，也不读取可变状态；c 为 nil
// （或不支持能力探测的 Chatter）时安全返回"未声明缓存支持"提示。
//
// 判定顺序确定：已启用优先，其次是本地端点，再是 OpenAI 兼容协议需显式开启，
// 接着是已探测到但未启用的 Anthropic/Responses 端点，最后兜底为未声明。
func CacheHintCapability(c llm.Chatter) string {
	if c == nil {
		return cacheHintUnknown
	}
	capability := llm.CacheSupport(c)
	if capability.Supported {
		return cacheHintEnabled
	}
	if capability.Local {
		return cacheHintLocal
	}
	switch capability.Protocol {
	case llm.ProtocolChatCompletions:
		return cacheHintOpenAINeedOptIn
	case llm.ProtocolAnthropicMessages, llm.ProtocolResponses:
		return cacheHintNotEnabled
	default:
		return cacheHintUnknown
	}
}

// CacheHintText 生成一行稳定的中文缓存摘要，形如
// "prompt cache 命中 30% (cached 30/100)"。
// 它是纯函数：只依赖 v 的字段，数值统一用 strconv.Itoa 转换，
// 拼接过程不按字节截断，因此对 CJK 安全且逐字节确定。
// 分母（PromptTokens）不可用时退化为固定的"统计不可用"文案。
func CacheHintText(v CacheVisibility) string {
	if v.PromptTokens <= 0 {
		return cacheHintStatsUnavailable
	}
	return "prompt cache 命中 " + strconv.Itoa(v.HitPercent) +
		"% (cached " + strconv.Itoa(v.CachedTokens) + "/" + strconv.Itoa(v.PromptTokens) + ")"
}
