package agent

import (
	"fmt"
	"strings"

	"example.com/argentgo/internal/llm"
)

// 中立记录中的工具结果是以 ToolCallID 为键的 Role:"tool" 消息。
// llm.Message 不携带结果的工具名或错误标志，因此缺失的名称会保留在结果文本中，
// 错误结果则用实时循环使用的同一个 "error: " 前缀标记（见 dispatch.go）。
const (
	missingToolResultText = "Tool result missing"
	emptyToolResultText   = "(empty tool result)"
	unknownToolName       = "unknown"
	unknownCallID         = "unknown"
)

// missingToolResult 为未得到回应的调用构建合成的错误结果。
// 因为 llm.Message 没有专门存放工具结果名称的字段，调用名会被折进文本中。
func missingToolResult(call llm.ToolCall) llm.Message {
	name := strings.TrimSpace(call.Function.Name)
	if name == "" {
		name = unknownToolName
	}
	id := strings.TrimSpace(call.ID)
	if id == "" {
		id = unknownCallID
	}
	return llm.Message{
		Role:       "tool",
		ToolCallID: call.ID,
		Content:    fmt.Sprintf("error: %s for tool %q (call %s)", missingToolResultText, name, id),
	}
}

// NormalizeToolPairing 修复记录*视图*，使每条助手工具调用在下一个用户/助手边界之前
// 都有匹配的工具结果。严格的提供方（尤其是 Anthropic）会拒绝 tool_use 块未被 tool_result
// 回应的请求，而这正发生在中断、批次取消或跨批次恢复之后。
//
// 这是一个附加性的纯视图层辅助函数：它从不修改输入，且在没有任何变化时返回原切片。
// 它刻意没有接入运行循环（循环已通过 fillUnrun 配对调用）；
// 调用方应在从会话读取消息之后、交给提供方之前调用它。
func NormalizeToolPairing(messages []llm.Message) []llm.Message {
	// pending 保存当前批次中尚未回应的调用；order 保持它们的原始顺序，
	// 使合成结果具有确定性。
	pending := make(map[string]*llm.ToolCall)
	var order []string

	// flush 回应当前批次中所有仍未回答的调用。结果必须与其 tool_use 相邻，
	// 因此被放置在边界之前（或记录末尾）的紧邻位置。
	flush := func(out *[]llm.Message) bool {
		if len(order) == 0 {
			return false
		}
		emitted := false
		for _, id := range order {
			call, ok := pending[id]
			if !ok {
				continue
			}
			*out = append(*out, missingToolResult(*call))
			emitted = true
		}
		pending = make(map[string]*llm.ToolCall)
		order = order[:0]
		return emitted
	}

	out := make([]llm.Message, 0, len(messages))
	changed := false

	for _, message := range messages {
		if message.Role == "tool" {
			if message.ToolCallID != "" {
				delete(pending, message.ToolCallID)
			}
			normalized := message
			if strings.TrimSpace(normalized.Content) == "" {
				normalized.Content = emptyToolResultText
				changed = true
			}
			out = append(out, normalized)
			continue
		}

		// 任何非工具消息都是边界：先回应未处理的调用。
		if flush(&out) {
			changed = true
		}

		if message.Role == "assistant" {
			for i := range message.ToolCalls {
				call := message.ToolCalls[i]
				if _, exists := pending[call.ID]; !exists {
					order = append(order, call.ID)
				}
				stored := call
				pending[call.ID] = &stored
			}
		}

		out = append(out, message)
	}

	if flush(&out) {
		changed = true
	}

	if !changed {
		return messages
	}
	return out
}
