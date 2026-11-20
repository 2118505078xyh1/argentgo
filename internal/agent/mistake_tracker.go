package agent

import (
	"strings"
	"sync"
)

// DefaultMistakeLimit 是未指定显式限制时构建 MistakeTracker 所使用的连续错误阈值。
const DefaultMistakeLimit = 3

// MistakeKind 对单次失败分类，使恢复指引能针对实际出错的原因量身定制。
// 三个标准值对应 agent 循环能观察到的失败：提供方/API 错误、模型产生的畸形工具调用，
// 以及已运行但报告失败的工具。
type MistakeKind string

const (
	MistakeKindAPIError            MistakeKind = "api_error"
	MistakeKindInvalidToolCall     MistakeKind = "invalid_tool_call"
	MistakeKindToolExecutionFailed MistakeKind = "tool_execution_failed"
)

// Valid 报告 k 是否为可识别的错误种类。空值或未知种类会被 Record 忽略，
// 因此传入零值的调用方不会意外抬高连续计数。
func (k MistakeKind) Valid() bool {
	switch k {
	case MistakeKindAPIError, MistakeKindInvalidToolCall, MistakeKindToolExecutionFailed:
		return true
	default:
		return false
	}
}

// MistakeGuidance 为当前运行构建恢复指令。它在构造时（NewMistakeTracker）
// 或之后（SetGuidance）注入，使追踪器保持为纯状态机，不感知提示或地区。
type MistakeGuidance func(kind MistakeKind, count int) string

// MistakeAction 是达到连续限制后返回的决定。Stop 为 false 时运行携带 Guidance
// （注入的恢复指令）继续；Stop 为 true 时没有可用指引，运行必须结束。
// Kind 和 Count 描述触发该决定的连续错误，使调用方无需重新读取追踪器即可记录或发出它。
type MistakeAction struct {
	Stop     bool
	Guidance string
	Kind     MistakeKind
	Count    int
}

// MistakeTracker 统计各类别的连续错误，直到调用方在一轮成功后调用 Reset。
// 它刻意与重复防护器分开：防护器检测*相同*调用的重复，而该追踪器检测*不同*失败的连续出现，
// 避免运行在一连串无关错误中徘徊。它可安全并发使用，因此并行运行的工具无需外部锁即可记录失败。
type MistakeTracker struct {
	mu       sync.Mutex
	limit    int
	count    int
	lastKind MistakeKind
	byKind   map[MistakeKind]int
	guidance MistakeGuidance
}

// NewMistakeTracker 用给定的连续限制构建追踪器（<= 0 的值选用 DefaultMistakeLimit）。
// 可选的指引回调在构造时注入；也可以用 SetGuidance 稍后设置。
func NewMistakeTracker(limit int, guidance ...MistakeGuidance) *MistakeTracker {
	if limit <= 0 {
		limit = DefaultMistakeLimit
	}
	t := &MistakeTracker{
		limit:  limit,
		byKind: make(map[MistakeKind]int, 3),
	}
	if len(guidance) > 0 {
		t.guidance = guidance[0]
	}
	return t
}

// SetGuidance 注入或替换恢复指引回调。传入 nil 会移除它，此时 Check 在达到限制时停止运行。
func (t *MistakeTracker) SetGuidance(fn MistakeGuidance) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.guidance = fn
}

// Record 把给定种类的一次错误加入当前连续序列。空或未知种类会被忽略。
// Record 可被多个 goroutine 安全地调用。
func (t *MistakeTracker) Record(kind MistakeKind) {
	if t == nil || !kind.Valid() {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.count++
	t.lastKind = kind
	t.byKind[kind]++
}

// Count 返回当前连续错误的长度。
func (t *MistakeTracker) Count() int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.count
}

// CountFor 返回当前连续序列中（即自上次 Reset 以来）种类 k 的错误数量。
func (t *MistakeTracker) CountFor(k MistakeKind) int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.byKind[k]
}

// Check 报告连续错误是否达到配置的限制。低于限制时返回 (false, 零值 action)。
// 达到或超过限制时返回的 action 中 Kind/Count 描述该连续序列：若设置了指引回调且返回非空指令，
// 运行携带该指引继续；否则运行必须停止。在调用 Reset 之前，每次调用都会重新评估决定，
// 因此何时清除连续序列由调用方掌控。指引回调在锁外调用，
// 以避免它回调追踪器时发生死锁。
func (t *MistakeTracker) Check() (shouldStop bool, action MistakeAction) {
	if t == nil {
		return false, MistakeAction{}
	}
	t.mu.Lock()
	if t.count < t.limit || t.limit <= 0 {
		t.mu.Unlock()
		return false, MistakeAction{}
	}
	kind, count, fn := t.lastKind, t.count, t.guidance
	t.mu.Unlock()

	action = MistakeAction{Kind: kind, Count: count}
	if fn != nil {
		if text := strings.TrimSpace(fn(kind, count)); text != "" {
			action.Guidance = text
			return false, action
		}
	}
	action.Stop = true
	return true, action
}

// Reset 清除连续序列及其按种类的细分。注入的指引和配置限制会保留；
// 调用方通常在一轮成功后调用 Reset，使下一次失败开启新的序列。
func (t *MistakeTracker) Reset() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.count = 0
	t.lastKind = ""
	for k := range t.byKind {
		delete(t.byKind, k)
	}
}

// 无效参数熔断器。
//
// 它是对 MistakeTracker 的补充而非替代。追踪器统计*所有*连续失败，且由于通常注入了指引，
// 它只会把信息重新喂给模型；从不强制硬停止。熔断器刻意很窄：只统计被工具判定为无效的参数
// （kilocode 报告中的 InvalidArgumentsError）。在单个用户轮次中三次这样的拒绝，
// 意味着模型没有从校验反馈中学习，因此熔断器发出必须结束该轮次的信号。
// 一次成功调用，或任何并非参数校验错误的失败，都证明有进展并立即清除计数。
//
// 分类（哪些工具错误算"无效参数"）和用户轮次边界（何时显式调用 Reset）由调用方掌管；
// 本类型是纯粹且并发安全的计数器，不感知工具或事件。

// DefaultInvalidArgThreshold 是未指定显式（正）阈值时熔断器使用的跳闸点。
const DefaultInvalidArgThreshold = 3

// InvalidArgBreakerCap 限制内部计数器，使一连串无效参数结果不会让它无限增长。
// 计数器达到上限后即饱和；ShouldStop 在 Reset 之前保持为 true。
// 请求的阈值高于上限时会被收紧到上限，从而保证熔断器始终可跳闸。
const InvalidArgBreakerCap = 64

// InvalidArgOutcome 是熔断器对一次工具结果的视图。把分类保留在调用方，
// 使 dispatch 能复用其现有的错误检查（工具校验错误、i18n 的 "error.invalid_args" 等），
// 而无需让本类型依赖 tools、i18n 或错误字符串。
type InvalidArgOutcome uint8

const (
	// InvalidArgOutcomeNone 是中性结果（例如未运行、被防护或已取消的调用）。
	// 它既不计数也不清除——调用从未到达工具，因此无法说明模型的参数质量。
	InvalidArgOutcomeNone InvalidArgOutcome = iota
	// InvalidArgOutcomeInvalidArgs 表示工具判定参数无效而拒绝。
	// 这是唯一会推进熔断器的结果。
	InvalidArgOutcomeInvalidArgs
	// InvalidArgOutcomeSuccess 表示工具成功运行。因为模型取得了进展，它会清除计数。
	InvalidArgOutcomeSuccess
	// InvalidArgOutcomeOtherFailure 表示工具运行了，但因与参数校验无关的原因失败。
	// 它会清除计数：这里的约束专门针对反复出现的无效参数，
	// 无关失败并不能证明模型在无视校验反馈。
	InvalidArgOutcomeOtherFailure
)

// InvalidArgBreaker 统计单个用户轮次内的无效参数拒绝，并在达到配置阈值时报告。
// 它可安全并发使用，因此并行运行的工具无需外部锁即可观察结果。
type InvalidArgBreaker struct {
	mu        sync.Mutex
	threshold int
	count     int
}

// NewInvalidArgBreaker 用给定阈值构建熔断器。非正阈值选用 DefaultInvalidArgThreshold；
// 高于 InvalidArgBreakerCap 的阈值会被收紧到该上限。
func NewInvalidArgBreaker(threshold int) *InvalidArgBreaker {
	if threshold <= 0 {
		threshold = DefaultInvalidArgThreshold
	}
	if threshold > InvalidArgBreakerCap {
		threshold = InvalidArgBreakerCap
	}
	return &InvalidArgBreaker{threshold: threshold}
}

// Observe 把一次工具结果纳入熔断器，并返回是否达到阈值。
// 这是供 dispatch 使用的唯一入口：对调用结果分类、传到这里，结果为 true 时结束该轮次。
// 在 Reset 之前，每次调用都会重新评估决定，因此何时清除计数由调用方掌控。
func (b *InvalidArgBreaker) Observe(outcome InvalidArgOutcome) (shouldStop bool) {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	switch outcome {
	case InvalidArgOutcomeInvalidArgs:
		if b.count < InvalidArgBreakerCap {
			b.count++
		}
	case InvalidArgOutcomeSuccess, InvalidArgOutcomeOtherFailure:
		// 两者都证明有进展（或至少不是参数错误），因此立即清除连续序列。
		b.count = 0
	case InvalidArgOutcomeNone:
		// 中性：保持计数不变。
	}
	return b.count >= b.threshold
}

// RecordInvalidArgs 是报告一次无效参数拒绝的便捷包装。它返回与 Observe 相同的停止信号。
func (b *InvalidArgBreaker) RecordInvalidArgs() bool {
	return b.Observe(InvalidArgOutcomeInvalidArgs)
}

// Reset 清除无效参数计数。调用方通常在用户轮次边界调用它；
// 成功和非参数失败已通过 Observe 自动重置。配置的阈值会保留。
func (b *InvalidArgBreaker) Reset() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.count = 0
}

// ShouldStop 报告是否已达到阈值，但不记录新结果。
func (b *InvalidArgBreaker) ShouldStop() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.count >= b.threshold
}

// Count 返回当前的无效参数计数（在上限处饱和）。
func (b *InvalidArgBreaker) Count() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.count
}

// Threshold 返回经过默认值处理和收紧后的有效跳闸点。
func (b *InvalidArgBreaker) Threshold() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.threshold
}
