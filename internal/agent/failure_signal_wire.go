package agent

// 本文件为 R14 的 ExtractFailureSignals 提供一个 opt-in 接线包装，
// 让主会话可以"选用"失败信号来增强压缩证据，而默认压缩行为逐字节不变。
//
// 设计约束（与 failure_signal.go 文件头一致）：本文件只做类型转换与门控，
// 不接入 Compact / CollectCompactionEvidence，也不持有任何全局开关。
// 是否启用由调用方在接线点显式传 enabled=true 决定；关闭时返回零值证据，
// 与从未调用本文件完全等价。

// SignalsToEvidence 把失败信号转换成压缩证据的 Failures 列表。
//
// 字段映射：
//   - FailureSignal.Text  -> Failure.Reason（已由提取侧按 rune 截断，CJK 安全）
//   - FailureSignal.Turn  -> Failure.Turn
//   - FailureSignal.Tool  -> Failure.Tool
//
// Failure.Error 刻意保持零值 false：失败信号来自工具结果与消息文本，
// 对应 CollectCompactionEvidence 中 EventToolEnd && IsError 的分支（Error=false）；
// Error=true 专用于提供方级别的 EventError，而本接线观察不到该类事件，
// 因此不能凭 Kind 伪造该语义。
//
// 本函数是纯类型转换：不改入参、按输入顺序稳定映射、空输入返回零值证据
// （Failures 为 nil，与 CollectCompactionEvidence 的空结果一致）。
func SignalsToEvidence(sigs []FailureSignal) CompactionEvidence {
	if len(sigs) == 0 {
		return CompactionEvidence{}
	}
	failures := make([]Failure, 0, len(sigs))
	for _, sig := range sigs {
		failures = append(failures, Failure{
			Turn:   sig.Turn,
			Tool:   sig.Tool,
			Reason: sig.Text,
		})
	}
	return CompactionEvidence{Failures: failures}
}

// FailureSignalsForCompaction 是失败信号接入压缩证据的门控入口。
//
// enabled=false（默认）时直接返回零值证据：不提取、不去重、不分配切片，
// 压缩行为与接线前逐字节一致。enabled=true 时依次执行
// 提取 -> 按 Text 去重（沿用 DeduplicateSignals 的稳定保序语义）-> 类型转换。
//
// messages 为 SignalInput 切片（MessageLike 是其别名）；函数只读入参，
// 不修改 messages 及其中的字符串，对 CJK / emoji 安全，结果顺序稳定。
func FailureSignalsForCompaction(messages []SignalInput, enabled bool) CompactionEvidence {
	if !enabled {
		return CompactionEvidence{}
	}
	return SignalsToEvidence(DeduplicateSignals(ExtractFailureSignals(messages)))
}
