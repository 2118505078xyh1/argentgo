package agent

import (
	"context"
	"fmt"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

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

// compactRetryWindow 给出压缩重试时传给 Compactor 的目标窗口。
// 配置了阈值时复用轮次中压缩的 0.8 迟滞带，使一次压缩留出余量；
// 未配置阈值时退回到上一次请求的提示估算（首次请求即溢出时为零，回落到最小窗口）。
func (s *runState) compactRetryWindow() int {
	if s.compactThreshold > 0 {
		window := int(float64(s.compactThreshold)*compactionRecoveryBand) - summaryReserveTokens
		if window < minCompactWindow {
			window = minCompactWindow
		}
		return window
	}
	window := int(float64(s.promptTokens) * compactionRecoveryBand)
	if window < minCompactWindow {
		window = minCompactWindow
	}
	return window
}

// compactAndReplay 处理一次由模型请求暴露的上下文溢出：压缩记录后在同一个轮次重发一次请求。
//
// 返回的 retried 表示是否真的重放了请求。调用方仅在 retried 为真时用 (reply, err) 取代
// 首次请求的结果；为假时必须保持原有的失败路径逐字节不变。
//
// 保守性：只有 opt-in 开关已开启、配置了 Compactor、失败类别为上下文溢出且
// DecideCompactRetry 判定仍有预算时才行动；压缩没有真正缩短记录时也不会白放一次请求。
// 计数器在重放之前自增，因此无论重放成功还是再次失败，每次运行最多只重放一次，
// 同一份超长请求不会反复空转。
func (s *runState) compactAndReplay(ctx context.Context, turn int, failure llm.FailureInfo) (llm.Message, error, bool) {
	if !s.opts.EnableCompactRetry || s.compactor == nil {
		return llm.Message{}, nil, false
	}
	if DecideCompactRetry(failure.Kind, s.compactRetries, MaxCompactRetries) != CompactRetry {
		return llm.Message{}, nil, false
	}

	before := EstimateMessages(s.messages)
	window := s.compactRetryWindow()
	compacted, changed := s.compactor.MaybeCompact(s.messages, window)
	if !changed || len(compacted) == 0 {
		return llm.Message{}, nil, false
	}
	s.messages = compacted
	// 摘要后的上下文是一个新窗口：与轮次中压缩一样清除提醒投递状态并刷新提示估算，
	// 使重放之后的预算检查看到真实的历史大小，而不是旧的超长记录。
	s.resetBudgetReminder()
	s.promptTokens = EstimateMessages(s.messages)
	s.compactRetries++
	s.emit(Event{Kind: EventNotice, Text: fmt.Sprintf(
		"上下文溢出：已将记录从 ~%d 压缩到 ~%d tokens，重放第 %d 轮", before, s.promptTokens, turn,
	)})

	// 重放同样是一次模型请求：和首次请求一样先经过持久性请求屏障。
	// 屏障错误会作为本次重放的结果返回，由调用方按既有失败路径分类并发出事件。
	if err := s.runBeforeRequest(); err != nil {
		return llm.Message{}, err, true
	}
	reply, err := s.chatWithFallback(ctx, turn)
	return reply, err, true
}
