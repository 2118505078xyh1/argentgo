package agent

import (
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/security"
)

// toolArgFeedback 控制「模型发出的工具参数不是合法 JSON 时，不执行工具并把
// 原始片段作为错误结果回灌」。默认开启、ARGENT_TOOL_ARG_FEEDBACK=0 关闭：
// 关闭时畸形参数仍按历史行为回退为 "{}" 后派发，消息与事件逐字节不变。
var toolArgFeedback atomic.Bool

// SetToolArgFeedback 由 app 按 ARGENT_TOOL_ARG_FEEDBACK 接线。
func SetToolArgFeedback(enabled bool) { toolArgFeedback.Store(enabled) }

// toolArgFeedbackEnabled 报告纠错回灌是否开启。
func toolArgFeedbackEnabled() bool { return toolArgFeedback.Load() }

// invalidArgRawLimit 限制回灌片段的字符数，避免一段失控的畸形输出占满上下文。
const invalidArgRawLimit = 200

// invalidArgsReason 生成模型可读且不执行的错误结果。原始片段先脱敏再按 rune
// 限长，避免把凭据回灌给模型，也避免在中文字节中间切断。
func invalidArgsReason(call llm.ToolCall) string {
	raw := security.RedactToolOutput(call.InvalidArguments)
	runes := []rune(raw)
	if len(runes) > invalidArgRawLimit {
		raw = string(runes[:invalidArgRawLimit]) + "..."
	}
	return fmt.Sprintf("tool arguments were malformed and the tool was not executed; "+
		"resend the call with valid JSON. raw input: %q", raw)
}

// splitInvalidArgCalls 在纠错回灌开启时把畸形参数的调用从批次中分离出来。
// 关闭时原样返回入参，调用方后续的每次比较、事件与消息都逐字节不变。
func splitInvalidArgCalls(calls []llm.ToolCall) (invalid, valid []llm.ToolCall) {
	if !toolArgFeedbackEnabled() {
		return nil, calls
	}
	for _, call := range calls {
		if strings.TrimSpace(call.InvalidArguments) != "" {
			invalid = append(invalid, call)
		} else {
			valid = append(valid, call)
		}
	}
	return invalid, valid
}
