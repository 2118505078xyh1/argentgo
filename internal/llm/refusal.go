package llm

import "strings"

// providerErrorNotExecutedText 是回放时对「流错误占位」的固定改写文本。
// 只保留「未执行」这一事实，绝不把提供方的拒绝解释喂回分类器（TODO #30）。
const providerErrorNotExecutedText = "[未执行：提供方错误，工具调用未执行]"

// parseRefusalReason 把线协议中的停止/过滤原因转为结构化拒绝字段。
// 只有明确的拒绝类别才返回非空值；"stop"/"end_turn"/"tool_calls"/"length"
// 等普通原因一律返回 nil，使普通回复的转录字节保持不变。
func parseRefusalReason(reason string) *ProviderRefusal {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "refusal", "sensitive", "content_filter", "content_filtered":
		return &ProviderRefusal{Reason: strings.ToLower(strings.TrimSpace(reason))}
	default:
		return nil
	}
}

// isProviderRefusal 报告一条助手消息是否是提供方的拒绝/过滤回复。
// 同时接受结构化 Refusal 字段与旧的 FinishReason 字符串，使从历史转录
// （尚未带结构化字段）恢复的会话也能被正确过滤。
func isProviderRefusal(message Message) bool {
	if message.Role != "assistant" {
		return false
	}
	if message.Refusal != nil {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(message.FinishReason)) {
	case "refusal", "sensitive", "content_filter", "content_filtered":
		return true
	default:
		return false
	}
}

// isSyntheticStopError 判定一条消息是否为流中断产生的占位（而非模型真实输出）。
// 占位由上层以 __synthetic 标记 + source=assistant_stop_error 写入，
// 这里按载荷中的键做保守判定，避免误伤正常消息。
func isSyntheticStopError(message Message) bool {
	for _, part := range []string{message.Content, message.Reasoning} {
		if strings.Contains(part, "__synthetic") && strings.Contains(part, "assistant_stop_error") {
			return true
		}
	}
	return false
}

// providerReplayMessages 把对话投影为可安全回放到提供方的消息序列：
//   - 丢弃被拒绝的 assistant 轮次，避免一次内容策略拒答在 resume 后污染
//     后续每一轮（自我污染）；
//   - 把流错误占位改写为固定的「未执行」文本，只保留事实、不携带拒绝理由。
//
// 该函数是纯函数，不改写入参；无拒绝轮次与占位时返回与原切片等价的副本。
func providerReplayMessages(msgs []Message) []Message {
	out := make([]Message, 0, len(msgs))
	for _, message := range msgs {
		if isProviderRefusal(message) {
			continue
		}
		if isSyntheticStopError(message) {
			message.Content = providerErrorNotExecutedText
			message.Reasoning = ""
			message.ToolCalls = nil
			message.Refusal = nil
		}
		out = append(out, message)
	}
	return out
}
