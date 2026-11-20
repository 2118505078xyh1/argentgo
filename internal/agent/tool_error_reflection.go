package agent

import (
	"strings"
	"sync/atomic"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

const (
	// toolErrorLabelFallback 在调用方没有给出标签时使用的默认工具类别名。
	toolErrorLabelFallback = "TOOL"

	// toolErrorFixInstruction 放在工具错误反射反馈最前面的可操作指令：
	// 它把这条结果明确标记为"修复后重试"的请求，而不是一条被动的状态行。
	// 这是与 verifyFixInstruction 平行的新模板；刻意保留中文，与任务约定一致。
	toolErrorFixInstruction = "修复以下工具调用错误后重试："

	// toolErrorReflectionMaxBytes 限制一条反射反馈中错误正文的字节数。
	// 工具错误通常很短；取验证默认预算的一半，既足以呈现完整错误，又不会占用过多上下文。
	toolErrorReflectionMaxBytes = defaultVerifyMaxBytes / 2
)

// toolErrorReflection 把一次工具调用/编辑格式错误包装成可操作的反射反馈。
// 输出布局与 VerificationFeedbackAt 对齐：指令行 → 带标签的错误头 → 脱敏且有界的错误正文。
//
//   - label 通常取工具名（如 "edit"），会被大写化；为空时回退为 toolErrorLabelFallback。
//   - errMsg 先去除首尾空白，再经 llm.RedactText 脱敏；为空（或仅含空白）时返回空串，
//     因为没有可操作的内容可供反射，调用方据此可以跳过注入。
//   - 错误正文通过 clipVerifyTail 裁剪：切点落在 rune 边界上（CJK 安全），并带上截断标记。
//   - 有界长度作用于错误正文；加上固定的指令行与错误头，整条反馈的总长度同样有界。
//
// 该函数是纯函数：无 I/O、无共享状态，相同输入总是产生逐字节相同的输出。
func toolErrorReflection(label, errMsg string) string {
	label = strings.ToUpper(strings.TrimSpace(label))
	if label == "" {
		label = toolErrorLabelFallback
	}
	clean := llm.RedactText(strings.TrimSpace(errMsg))
	if clean == "" {
		return ""
	}
	head := "[tool: " + label + "] ERROR"
	return toolErrorFixInstruction + "\n" + head + "\n" + clipVerifyTail(clean, toolErrorReflectionMaxBytes)
}

// toolErrorReflectionOptIn 控制是否把工具错误接入有界反射回路，默认关闭。
// 与工具名还原/参数强转一样，这是进程级 opt-in，由 app 层在启动时统一设置：
// 接线会改变模型看到的反馈内容，因此绝不能悄悄引入既有运行。
var toolErrorReflectionOptIn atomic.Bool

// SetToolErrorReflection 打开或关闭工具错误反射接线。
// 关闭（默认）时，AugmentToolErrorReflection 逐字节原样返回工具结果，既有行为不变。
func SetToolErrorReflection(enabled bool) {
	toolErrorReflectionOptIn.Store(enabled)
}

// toolErrorReflectionEnabled 报告当前是否启用了工具错误反射接线。
func toolErrorReflectionEnabled() bool {
	return toolErrorReflectionOptIn.Load()
}

// AugmentToolErrorReflection 是供接线点使用的一次性门控：
// 仅当 opt-in 已开启且 output 确实是一次错误时，才把它包装成可操作的反射反馈；
// 关闭（默认）、非错误或包装结果为空时，逐字节原样返回 output。
//
// 建议接线位置：internal/agent/dispatch.go 中 runTool 的错误分支
// （"error: " + err.Error() 与 isRecoverablePlanBlock 处理附近），在把结果写回对话之前调用。
// 这样工具调用/编辑格式错误就能进入与验证 FAIL 相同的有界反思路径，
// 而不是仅作为一条工具结果被静默消费。由于该函数是纯门控，是否真正注入下一轮次
// 仍由调用方按现有反射回路的预算决定，本包不擅自改变循环行为。
func AugmentToolErrorReflection(label, output string, isError bool) string {
	if !isError || !toolErrorReflectionEnabled() {
		return output
	}
	if reflected := toolErrorReflection(label, output); reflected != "" {
		return reflected
	}
	return output
}
