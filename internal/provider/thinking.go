package provider

import (
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// ThinkingSupport 返回的思考支持种类。它们描述线上协议如何表达推理控制，而非是否启用。
// 协议如何表达推理控制，而非是否启用。
const (
	ThinkingEffort = "effort" // a named level (reasoning_effort / reasoning.effort)
	ThinkingToggle = "toggle" // a plain on/off switch
	ThinkingBudget = "budget" // an explicit token budget (Anthropic extended thinking)
	ThinkingNone   = "none"   // no reasoning control, or an unknown protocol
)

// ThinkingSupport 按推理控制形态对线上协议分类：
// control:
//
//   - "effort"：OpenAI chat-completions 和 Responses 使用命名级别
//   - "effort"：OpenAI chat-completions 和 Responses 使用 reasoning_effort / reasoning.effort 中的命名级别（"low"/"medium"/"high"）。
//   - "budget"：Anthropic Messages 在其扩展思考块上使用显式 budget_tokens 值。
//     扩展思考块。
//   - "toggle"：布尔开关。目前没有内置协议使用它；
//     为只暴露开关的 provider 保留，使
//   - "toggle"：布尔开关。目前没有内置协议使用它；为只暴露开关的 provider 保留，使 ResolveThinking 仍能为此类协议规范化请求。
//   - "none"：协议没有推理控制。这也是空或无法识别名称的回退。
//     也是空或无法识别名称的回退。
//
// 匹配忽略首尾空白和大小写。接受来自
// 匹配忽略首尾空白和大小写。接受来自 internal/llm 的规范线上名称和常见短 provider 别名。
// 四种支持种类本身也被接受并原样返回，
// 四种支持种类本身也被接受并原样返回，因此函数是幂等的，调用方可以传入已分类的
// 种类。这是纯函数：无 I/O 且无全局状态。
func ThinkingSupport(protocol string) string {
	normalized := strings.ToLower(strings.TrimSpace(protocol))
	switch normalized {
	case ThinkingEffort, ThinkingToggle, ThinkingBudget, ThinkingNone:
		return normalized
	case string(ProtocolOpenAI), "openai", "chat", "chat-completions":
		return ThinkingEffort
	case string(ProtocolResponses), "responses":
		return ThinkingEffort
	case string(ProtocolAnthropic), "anthropic", "messages":
		return ThinkingBudget
	default:
		return ThinkingNone
	}
}

// 思考预算边界。Anthropic 的扩展思考拒绝低于 1024 token 的预算；上限防止不合理的请求
// 思考预算边界。Anthropic 的扩展思考拒绝低于 1024 token 的预算；上限防止不合理的请求被转发，同时远高于任何当前模型的可用额度。
// 被转发，同时远高于任何当前模型的可用额度。
const (
	minThinkingBudget int64 = 1024
	maxThinkingBudget int64 = 200_000
)

// normalizeEffort 把用户提供的推理 effort 映射到 OpenAI 风格线上字段理解的规范小写级别。去掉首尾空白且匹配不区分大小写；常见 CJK 拼写
// OpenAI 风格线上字段理解的规范小写级别。去掉
// 首尾空白且匹配不区分大小写；常见 CJK 拼写
// are accepted so "--thinking 高" behaves like "--thinking high". An
// 无法识别或空值返回 ""，使绝不转发无效级别。
func normalizeEffort(effort string) string {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "low", "较低", "低", "低级":
		return "low"
	case "medium", "med", "middle", "中等", "中", "中级":
		return "medium"
	case "high", "较高", "高", "高级":
		return "high"
	default:
		return ""
	}
}

// clampBudget 限制思考 token 预算。非正值表示
// 无法识别或空值返回 ""，使绝不转发无效级别。
// clampBudget 限制思考 token 预算。非正值表示"未设置"并保持 0，使调用方完全不发出预算字段；正值被限制在 [minThinkingBudget, maxThinkingBudget]。
func clampBudget(budget int64) int64 {
	if budget <= 0 {
		return 0
	}
	if budget < minThinkingBudget {
		return minThinkingBudget
	}
	if budget > maxThinkingBudget {
		return maxThinkingBudget
	}
	return budget
}

// ResolveThinking 针对线上协议规范化用户的思考请求。它是
// ResolveThinking 针对线上协议规范化用户的思考请求。它是纯函数：无 I/O、不修改输入，且不依赖
// config 或 session 状态。返回的 llm.ThinkingConfig 是供未来线上客户端接线步骤使用的数据；在该步骤落地前，保持
// 供未来线上客户端接线步骤使用的数据；在该步骤落地前，保持
// llm.Options.Thinking 为 nil 使当前线上字节不变。
//
//   - effort 协议只保留规范化的 Effort 级别；
//   - budget 协议只保留限制后的 BudgetTokens 值；
//   - toggle 协议仅在请求要求 "on" 时设置 Enabled；
//   - 不支持的协议返回零 ThinkingConfig。
func ResolveThinking(protocol, effort string, budget int64) llm.ThinkingConfig {
	switch ThinkingSupport(protocol) {
	case ThinkingEffort:
		return llm.ThinkingConfig{Effort: normalizeEffort(effort)}
	case ThinkingBudget:
		return llm.ThinkingConfig{BudgetTokens: clampBudget(budget)}
	case ThinkingToggle:
		on := normalizeEffort(effort) != "" || budget > 0
		return llm.ThinkingConfig{Enabled: &on}
	default:
		return llm.ThinkingConfig{}
	}
}
