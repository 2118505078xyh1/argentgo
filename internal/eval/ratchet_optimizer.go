package eval

import (
	"fmt"
)

// 本文件提供 ratchet eval 爬山优化器的状态机核心。它只做「接受/拒绝候选」
// 这一纯决策：用训练集配对差值判断是否有提升，用保留集配对差值的置信下界
// 判断是否发生显著退化，两者同时满足才接受候选并让 BestScore 前进一格。
//
// 决策规则是纯函数式的：同样的输入与状态永远得到同样的 (accept, reason)，
// 不读时钟、网络、文件或任何全局可变状态，也不调用现有 golden/评分路径。
// Decide 唯一的副作用是按规则更新接收者自身的 BestScore 与 History。
// 目前没有调用方，独立于现有 golden 快照比对，不改变任何评分行为。

// defaultRatchetEpsilon 是判定保留集「显著退化」时使用的默认容忍度。
// 当调用方传入的 epsilon <= 0 时改用此默认值，避免阈值被误设成 0 后
// 把任何微小噪声都当作退化。
const defaultRatchetEpsilon = 0.01

// RatchetEntry 记录一次决策的上下文，便于事后审计爬山轨迹。
// 所有字段都是值类型（Delta 内部亦无引用成员），因此历史条目与入参互不共享可变状态。
type RatchetEntry struct {
	// TrainMeanDiff 是训练集配对差值均值，接受时须严格大于 0。
	TrainMeanDiff float64
	// HoldoutCILow 是保留集差值的 95% 置信下界，接受时须严格大于 -epsilon。
	HoldoutCILow float64
	// Epsilon 是本次决策实际使用的退化容忍度（可能已回退到默认值）。
	Epsilon float64
	// PrevBestScore 是决策前的 BestScore。
	PrevBestScore float64
	// BestScore 是决策后的 BestScore；拒绝时等于 PrevBestScore。
	BestScore float64
	// Accept 是本次决策是否接受候选。
	Accept bool
	// Reason 是人类可读的决策理由（中文）。
	Reason string
}

// RatchetState 是爬山优化器的状态：BestScore 单调不减，History 按接受顺序累积。
type RatchetState struct {
	BestScore float64
	History   []RatchetEntry
}

// NewRatchetState 以给定的初始分数构造一个空历史的状态。
func NewRatchetState(initial float64) *RatchetState {
	return &RatchetState{BestScore: initial}
}

// Decide 依据训练集与保留集的配对差值决定是否接受一个候选。
//
// 接受条件（须同时满足）：
//   - trainDelta.MeanDiff > 0：训练集上确有提升；
//   - holdoutDelta.CILow > -epsilon：保留集上没有超过容忍度的显著退化。
//     这里用置信下界而非均值，是为了给噪声留出余量：只要最坏情形的退化幅度
//     仍在 epsilon 之内，就认为候选没有真正伤害泛化表现。
//
// epsilon <= 0 时回退到 defaultRatchetEpsilon。边界为严格不等号，
// 即 holdoutDelta.CILow 恰好等于 -epsilon 时按退化处理（拒绝）。
//
// 接受时 BestScore 增加 trainDelta.MeanDiff（该值保证为正，因此 ratchet
// 严格前进、永不回退），并向 History 追加一条记录；拒绝时 BestScore 与
// History 均保持不变。两个入参 Delta 都按值传入且不被修改。
func (s *RatchetState) Decide(trainDelta, holdoutDelta Delta, epsilon float64) (accept bool, reason string) {
	if epsilon <= 0 {
		epsilon = defaultRatchetEpsilon
	}

	// 先判定拒绝条件，任何一条不满足都不触碰状态。
	if trainDelta.MeanDiff <= 0 {
		return false, fmt.Sprintf("拒绝：训练集未提升（MeanDiff=%.6f，须 > 0）", trainDelta.MeanDiff)
	}
	if holdoutDelta.CILow <= -epsilon {
		return false, fmt.Sprintf("拒绝：保留集显著退化（CILow=%.6f，须 > %.6f）", holdoutDelta.CILow, -epsilon)
	}

	// 到达这里说明两条接受条件都成立：让 ratchet 前进并记录轨迹。
	prevBest := s.BestScore
	newBest := prevBest + trainDelta.MeanDiff
	s.BestScore = newBest
	reason = fmt.Sprintf("接受：训练集提升 %.6f 且保留集未显著退化（CILow=%.6f > %.6f）",
		trainDelta.MeanDiff, holdoutDelta.CILow, -epsilon)
	s.History = append(s.History, RatchetEntry{
		TrainMeanDiff: trainDelta.MeanDiff,
		HoldoutCILow:  holdoutDelta.CILow,
		Epsilon:       epsilon,
		PrevBestScore: prevBest,
		BestScore:     newBest,
		Accept:        true,
		Reason:        reason,
	})
	return true, reason
}
