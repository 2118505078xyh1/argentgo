package agent

import "math"

// 本文件提供"压缩策略选择"纯原语：把一组上下文信号映射为离散的压缩动作。
// 它是纯函数、默认不接线——只有显式调用 SelectCompactionAction 时才会被用到，
// 不改动任何现有类型、事件或压缩实现，也不引入新的包间依赖（仅用标准库 math）。
//
// 设计动机（R42）：过去"何时 trim、何时 summarize、保留多少"的判断散落在压缩、
// 预算、上下文压力等多处，缺少一个可复用、可单测的决策原语。本文件把这套判断
// 收敛为一次纯计算，供上层（UI、日志、后续实际的压缩接线）共享，避免各处阈值不一致。

// CompactionAction 是压缩策略选择的输出动作，按对上下文的干预强度从弱到强排列：
// none（不动）→ trim_local（本地裁剪，丢弃部分原始工具结果）→
// summarize_model（模型摘要，保留关键信息）→ compact_full（整体压缩）。
type CompactionAction string

// 四个稳定动作值。取值固定，便于快照、日志与跨语言协议比对。
const (
	CompactionNone           CompactionAction = "none"
	CompactionTrimLocal      CompactionAction = "trim_local"
	CompactionSummarizeModel CompactionAction = "summarize_model"
	CompactionCompactFull    CompactionAction = "compact_full"
)

// 决策阈值。比例用浮点常量，严格大于（>）才算越过阈值，等于阈值不算。
const (
	compactionSummarizeRatio = 0.9 // 用量 > 90%：直接走模型摘要
	compactionTrimRatio      = 0.7 // 用量 > 70%：结合工具结果数量考虑本地裁剪

	// "大量工具结果"的门槛。低于该值，即便用量偏高也不值得为裁剪付出信息损失。
	compactionManyToolResults = 8
	// "错误较多"的门槛。达到该值即认为上下文里含有需要保留的错误证据。
	compactionManyErrors = 3
)

// CompactionInput 是策略选择的输入信号集合。字段均为值类型，
// SelectCompactionAction 只读不改——调用方持有的这份输入在调用后保持不变。
type CompactionInput struct {
	// UsageRatio 是已用上下文占预算的比例。允许任意浮点值；
	// NaN 视作 0（没有可用信号），±Inf 与越界值按 [0,1] 钳制。
	UsageRatio float64
	// ErrorCount 是近期累计的错误数，用于判断是否需要保留错误证据。
	ErrorCount int
	// ToolResults 是近期累计的工具结果条数，用于判断本地裁剪是否有收益。
	ToolResults int
	// BudgetExceeded 表示预算已被突破，优先级最高，必须立刻整体压缩。
	BudgetExceeded bool
}

// SelectCompactionAction 按输入信号选择一个压缩动作。
//
// 规则（按优先级从上到下）：
//  1. BudgetExceeded 为真 → compact_full。预算突破是硬约束，不受其他信号影响，
//     也不因"保留证据"而降级——此时必须先压缩回来。
//  2. 钳制后的 usage > 0.9 → summarize_model。用量已接近上限，交给模型摘要，
//     在压缩的同时保留关键语义。
//  3. 钳制后的 usage > 0.7 且工具结果达到"大量"门槛 → 默认 trim_local；
//     但若 ErrorCount 达到"错误较多"门槛，则升级为 summarize_model：
//     错误多意味着原始工具结果里含有排障证据，本地裁剪会直接丢掉这些证据，
//     因此改用模型摘要来"保留 evidence、不激进裁剪"。
//  4. 其余情况 → none，不引入任何额外开销。
//
// 该函数是纯函数：不读取全局状态、不产生 I/O、不修改入参（入参为值类型），
// 相同输入总产生相同动作。UsageRatio 先经 clampUsageRatio 归一化，
// 因此 NaN / ±Inf / 越界值不会导致非确定或越界的行为。
func SelectCompactionAction(in CompactionInput) CompactionAction {
	// 预算突破优先级最高，先于任何比例判断。
	if in.BudgetExceeded {
		return CompactionCompactFull
	}

	ratio := clampUsageRatio(in.UsageRatio)

	// 接近上限：模型摘要，保留关键语义。
	if ratio > compactionSummarizeRatio {
		return CompactionSummarizeModel
	}

	// 用量偏高且工具结果大量堆积时，本地裁剪才有明显收益。
	if ratio > compactionTrimRatio && in.ToolResults >= compactionManyToolResults {
		// 错误较多时保留证据：升级为模型摘要，避免本地裁剪直接丢弃排障证据。
		if in.ErrorCount >= compactionManyErrors {
			return CompactionSummarizeModel
		}
		return CompactionTrimLocal
	}

	return CompactionNone
}

// clampUsageRatio 把用量比例钳制到 [0,1]。
// NaN 没有明确的大小关系，统一视作 0（无信号），保证确定性；
// ±Inf 与越界值由 math.Max/math.Min 钳到区间端点。
func clampUsageRatio(r float64) float64 {
	if math.IsNaN(r) {
		return 0
	}
	if r < 0 {
		return 0
	}
	if r > 1 {
		return 1
	}
	return r
}
