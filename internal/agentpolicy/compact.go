package agentpolicy

import "example.com/argentgo/internal/llm"

// MaxCompactRetries 是循环针对提供方报告的上下文溢出，可以进行的有上限的
// "压缩后重试"次数。一次就足够：如果同一个超长请求在压缩后仍然溢出，
// 再次重放只会消耗轮次预算，而不会带来任何进展。
const MaxCompactRetries = 1

// CompactRetryDecision 是模型请求失败后循环的选择。"retry" 会把记录压缩一次并重发同一轮次；
// "stop" 则保持原有的失败路径不变。
type CompactRetryDecision string

const (
	CompactRetry CompactRetryDecision = "retry"
	CompactStop  CompactRetryDecision = "stop"
)

// DecideCompactRetry 是"压缩后重试"的纯决策层。它从不压缩、发出事件或修改状态；
// 这些副作用由调用方负责。
//
// 只有当提供方报告的上下文溢出且仍有重试预算时才允许重试。maxRetries <= 0 表示"完全不重试"，
// 因此未配置的调用方会保持停止。这里其他所有失败类型都是最终结论：传输和限流错误
// 在 chatWithFallback 中已有各自的模型切换路径，而重放相同的消息无法修复鉴权、
// 配置或校验失败。
func DecideCompactRetry(kind llm.FailureKind, retriesSoFar int, maxRetries int) CompactRetryDecision {
	if maxRetries <= 0 || kind != llm.FailureContextOverflow || retriesSoFar >= maxRetries {
		return CompactStop
	}
	return CompactRetry
}
