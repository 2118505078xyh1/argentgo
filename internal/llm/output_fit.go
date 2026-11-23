package llm

import (
	"github.com/2118505078xyh1/argentgo/internal/tokenize"
)

// 输出上限拟合的最优参数（来自 TODO #9 的竞品口径）：
//   - 下限 1024：即使提示几乎填满窗口，仍给模型留出可用的输出空间；
//   - 余量 64：为协议开销保留的固定 token；
//   - 误差系数 1.1：本地估算相对提供方 tokenizer 的 10% 安全余量；
//   - 每条消息 16 framing token：消息头/角色标记的固有开销。
const (
	fitOutputFloor         = 1024
	fitOutputMargin        = 64
	fitOutputErrorPercent  = 10
	fitOutputFramingPerMsg = 16
)

// FitOutputTokens 把期望输出上限 maxTokens 拟合进 contextWindow，返回本次请求
// 真正可用的输出 token 上限。它只收紧、不放宽：拟合结果不会超过 maxTokens。
//
// 提示 token 的计算复用最近一条携带 Usage.PromptTokens 的助手消息作为权威
// 锚点（提供方计费数），只对其后追加的消息用 estimate 做本地估算；这样避免
// 对整段历史重复估算带来的口径漂移。没有锚点时全部消息走本地估算。
// estimate 为 nil 时使用 tokenize.Estimate。
//
// contextWindow <= 0（未知窗口）或 maxTokens <= 0 时直接返回 maxTokens，
// 使没有窗口信息的调用方逐字节保持原行为。Anthropic 协议在窗口边界处由宿主
// 停止生成，不需要本拟合，调用方应跳过（见 TODO #9 的短路约定）。
func FitOutputTokens(maxTokens, contextWindow int, msgs []Message, estimate func(string) int) int {
	if maxTokens <= 0 || contextWindow <= 0 {
		return maxTokens
	}
	prompt := anchoredPromptTokens(msgs, estimate)
	available := contextWindow - prompt - fitOutputMargin
	if available < fitOutputFloor {
		available = fitOutputFloor
	}
	if available > maxTokens {
		available = maxTokens
	}
	return available
}

// anchoredPromptTokens 返回提示 token 估计值。最近一条带正向
// Usage.PromptTokens 的助手消息作为锚点，只估算其后的尾部消息；
// 本地估算部分额外乘 1.1 误差余量并加每条 16 framing token。
func anchoredPromptTokens(msgs []Message, estimate func(string) int) int {
	if estimate == nil {
		estimate = tokenize.Estimate
	}
	anchor := 0
	start := 0
	for i, message := range msgs {
		if message.Role == "assistant" && message.Usage != nil && message.Usage.PromptTokens > 0 {
			anchor = message.Usage.PromptTokens
			start = i + 1
		}
	}
	tail := msgs[start:]
	if len(tail) == 0 {
		return anchor
	}
	tokenPart := 0
	for _, message := range tail {
		tokenPart += estimate(message.Content)
		for _, call := range message.ToolCalls {
			tokenPart += estimate(call.Function.Name) + estimate(call.Function.Arguments)
		}
	}
	// 1.1 倍误差余量只作用于 token 估算部分；framing 是固定开销，不放大。
	withError := tokenPart + (tokenPart+fitOutputErrorPercent-1)/fitOutputErrorPercent
	return anchor + withError + len(tail)*fitOutputFramingPerMsg
}

// fitChatMaxTokens 为 chat completions 请求计算拟合后的输出上限。
// 返回 0 表示无需改写请求（未配置窗口，或拟合后仍不小于默认上限），
// 从而让绝大多数请求的线协议字节保持不变。
func fitChatMaxTokens(window int, msgs []Message) int {
	if window <= 0 {
		return 0
	}
	fitted := FitOutputTokens(DefaultChatMaxTokens, window, msgs, tokenize.Estimate)
	if fitted >= DefaultChatMaxTokens {
		return 0
	}
	return fitted
}
