package session

import "strconv"

// replay_wire.go 把重放得到的状态投影成可注入恢复公告的简洁结构。
//
// 这里只是 stdlib-only 的纯适配器：它把所有行为都建立在 ReplayState 之上，
// 零 I/O、不改文件、不调用模型，也不 import llm/agent/app。函数本身没有
// 调用方，存在意义是给上层提供一个稳定、可单测的"重放 → 恢复公告"接缝。
// 关键约束：合成的中止结果永远不算真实应答。allowIncomplete 只能放宽
// "是否自动继续"的决策，不会把未配对的工具调用改写成完整状态。

const (
	// recoveryHintCompleteText 是无未配对工具调用时的固定公告字面量。
	// 保持为常量，便于上层按字面量做稳定的本地化映射。
	recoveryHintCompleteText = "会话完整"
)

// RecoveryHint 是 ReplayState 的只读投影，可直接注入恢复公告。
//
// 它不持有任何切片，字段在构造时按值快照；因此调用方拿到的结构不与原
// ReplayState 的底层数组共享，后续改动原切片不会影响这些字段。
type RecoveryHint struct {
	// HasUnpaired 报告流中是否存在未配对的工具调用。
	HasUnpaired bool
	// UnpairedCount 是未配对工具调用的个数。
	UnpairedCount int
	// Complete 原样镜像 ReplayState.Complete：它同时受未配对调用与 parent
	// 环影响，不受是否允许不完整继续的影响。
	Complete bool
	// Text 是给用户看的稳定中文公告。无未配对时为固定字面量
	// "会话完整"；有未配对时为 "有 N 个未完成的工具调用"。
	Text string
}

// HintFromReplay 把 ReplayState 投影成 RecoveryHint。
//
// 它只读入参：既不会修改 st.UnpairedToolCalls 及其底层数组，也不会因为
// st.Complete 为假就改变 Text 的判定口径——Text 只按未配对调用的数量
// 决定。这样"因 parent 环而不完整"的流即使没有未配对调用，公告仍是
// "会话完整"（环属于对账/结构问题，不在工具调用公告的职责范围内）。
func HintFromReplay(st ReplayState) RecoveryHint {
	hint := RecoveryHint{
		HasUnpaired:   len(st.UnpairedToolCalls) > 0,
		UnpairedCount: len(st.UnpairedToolCalls),
		Complete:      st.Complete,
		Text:          recoveryHintCompleteText,
	}
	if hint.HasUnpaired {
		hint.Text = "有 " + strconv.Itoa(hint.UnpairedCount) + " 个未完成的工具调用"
	}
	return hint
}

// CanAutoContinue 报告是否可以在无需新的人工决策的情况下自动继续。
//
// 仅当重放状态本身完整，或调用方显式允许不完整流时返回 true。关键语义：
// allowIncomplete 只放宽这一决策，绝不把未配对的工具调用当作真实的
// 应答。没有结果的工具调用在恢复时只会由合成的中止结果应答；是否据此
// 自动继续由调用方通过该标志决定，但 HintFromReplay 投影出的 HasUnpaired、
// UnpairedCount、Complete 与 Text 在任何取值下都不因此被改写。
func CanAutoContinue(st ReplayState, allowIncomplete bool) bool {
	return st.Complete || allowIncomplete
}
