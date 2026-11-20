package agent

import (
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// ResponseKind 是单次模型回复的完整性类别。它根据回复实际携带的内容
// （工具部分、可见文本、推理）以及提供方的结束原因推导——绝不依赖 Result.Stopped，
// 因为循环只在运行结束后才写入它。
//
// 四种主要类别对应 OpenHands/kilocode 的回复审计：
//
//	ResponseToolCalls      存在一个或多个工具部分；
//	ResponseContent        收到了可见答案，且没有工具部分；
//	ResponseReasoningOnly  只收到了私有推理（和/或用量）；
//	ResponseEmpty          完全没有收到可用内容。
//
// ResponseToolCallsMissing 是防御性分类，并非第五种答案：提供方宣告了一个工具轮次，
// 却没有返回任何工具部分。这样的回复必须降级为正常停止，绝不能作为工具轮次派发，
// 也不能用于触发又一次提示——否则循环会用未改变的记录重新提示，一直空转到轮次预算耗尽。
//
// 数值从 1 开始，因此零值不是有效类别；忘记分类的调用方不会意外看起来像一个健康的答案。
type ResponseKind uint8

const (
	ResponseToolCalls ResponseKind = iota + 1
	ResponseContent
	ResponseReasoningOnly
	ResponseEmpty
	ResponseToolCallsMissing
)

// isToolCallsFinish 报告提供方的结束原因是否宣告了一个工具轮次。
// 它覆盖所支持协议的写法：OpenAI 的 "tool_calls"、Anthropic 的 "tool_use"，
// 以及 Responses API 的 "function_call" / "function_calls"。
// 匹配不区分大小写和空格，与 dispatch.go 中的 isLengthReason 一致。
func isToolCallsFinish(reason string) bool {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "tool_calls", "tool_use", "function_call", "function_calls":
		return true
	default:
		return false
	}
}

// ClassifyResponse 是纯函数：只读取参数，不做 I/O，也不修改状态。
// 用量/token 元数据刻意不在输入中，因此只带推理或只带用量记录的回复
// 不会被误认为可见答案。
//
// 优先级是刻意设计的：
//
//  1. 真实的工具部分优先于任何结束原因——被截断的工具批次仍然是工具批次
//     （长度由循环单独处理）。
//  2. 宣告了工具却未交付任何工具的结束原因是降级信号；它高于内容/推理，
//     使调用方停止，而不是把畸形的工具轮次再推回另一个相同的请求。
//  3. 可见内容高于推理：一段简短的文本答案伴随长篇思考时属于 CONTENT 回复，
//     而非纯推理回复。
func ClassifyResponse(hasToolCalls, hasVisibleContent, hasReasoning bool, finishReason string) ResponseKind {
	if hasToolCalls {
		return ResponseToolCalls
	}
	if isToolCallsFinish(finishReason) {
		return ResponseToolCallsMissing
	}
	if hasVisibleContent {
		return ResponseContent
	}
	if hasReasoning {
		return ResponseReasoningOnly
	}
	return ResponseEmpty
}

// ClassifyReply 从回复中派生三个内容标志，并委托给 ClassifyResponse。
// 它是决定"可见内容"含义的唯一入口：推理和用量是记录元数据，不是答案。
// 空白（包括全角空格等 Unicode 空白）不算数，因此仅由空白文本构成的回复属于 EMPTY，
// 可由调用方纠正。
func ClassifyReply(reply llm.Message) ResponseKind {
	return ClassifyResponse(
		len(reply.ToolCalls) > 0,
		strings.TrimSpace(reply.Content) != "",
		strings.TrimSpace(reply.Reasoning) != "",
		reply.FinishReason,
	)
}

// NeedsCorrection 报告某回复是否为无工具、无内容的轮次，调用方可以提示模型重新产出可用答案。
// 纯推理和空回复符合；畸形的工具轮次刻意不符合：它必须降级为停止，
// 以免重新发送未改变的记录。
func (k ResponseKind) NeedsCorrection() bool {
	return k == ResponseReasoningOnly || k == ResponseEmpty
}

// IsToolTurn 报告该回复是否应作为工具批次派发。
// 这里 ResponseToolCallsMissing 为 false——这就是安全地降级为停止。
func (k ResponseKind) IsToolTurn() bool {
	return k == ResponseToolCalls
}

// IsDowngraded 报告结束原因是否宣告了一个并未实际出现的工具轮次。
// 调用方应把这样的回复当作安全的停止。
func (k ResponseKind) IsDowngraded() bool {
	return k == ResponseToolCallsMissing
}

// String 为日志和诊断提供稳定的英文名称。
func (k ResponseKind) String() string {
	switch k {
	case ResponseToolCalls:
		return "tool_calls"
	case ResponseContent:
		return "content"
	case ResponseReasoningOnly:
		return "reasoning_only"
	case ResponseEmpty:
		return "empty"
	case ResponseToolCallsMissing:
		return "tool_calls_missing"
	default:
		return "unknown"
	}
}
