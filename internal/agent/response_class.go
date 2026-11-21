package agent

import (
	"github.com/2118505078xyh1/argentgo/internal/agentpolicy"
	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// ResponseKind 是单次模型回复的完整性类别。类型定义与分类逻辑已下沉到
// internal/agentpolicy（纯策略叶子包）；这里保留别名，使既有调用点与对外 API 不变。
//
// 四种主要类别：ResponseToolCalls（有工具部分）、ResponseContent（可见答案）、
// ResponseReasoningOnly（仅私有推理）、ResponseEmpty（无可用内容）；
// ResponseToolCallsMissing 是防御性分类：提供方宣告工具轮次却未交付工具，
// 必须降级为安全停止，绝不重新提示未改变的记录。
type ResponseKind = agentpolicy.ResponseKind

const (
	ResponseToolCalls        = agentpolicy.ResponseToolCalls
	ResponseContent          = agentpolicy.ResponseContent
	ResponseReasoningOnly    = agentpolicy.ResponseReasoningOnly
	ResponseEmpty            = agentpolicy.ResponseEmpty
	ResponseToolCallsMissing = agentpolicy.ResponseToolCallsMissing
)

// ClassifyResponse 委托给 agentpolicy 的纯分类实现。
func ClassifyResponse(hasToolCalls, hasVisibleContent, hasReasoning bool, finishReason string) ResponseKind {
	return agentpolicy.ClassifyResponse(hasToolCalls, hasVisibleContent, hasReasoning, finishReason)
}

// ClassifyReply 委托给 agentpolicy 的纯分类实现。
func ClassifyReply(reply llm.Message) ResponseKind {
	return agentpolicy.ClassifyReply(reply)
}
