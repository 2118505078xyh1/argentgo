package eval

// 本文件提供"本轮测量质量"的汇总原语：把样本量与显著性折算成
// MeasurementQuality，用稳定的不完整原因分类与 Warnings 把监控盲区显式化。
// 与 compare_report.go 的 incomplete-reason 分类一脉相承（R38/R39），
// 但此处是独立的纯函数：不修改入参、不读时钟/网络/全局状态，
// 也不触碰 golden/评分路径，更不接线到 CLI。

// 稳定的不完整原因分类标签。用常量而非散落字面量，保证跨版本输出一致；
// "完整"表示样本充足且差异显著，即没有监控盲区。
const (
	qualityReasonInsufficient   = "样本不足"
	qualityReasonNotSignificant = "差异不显著"
	qualityReasonComplete       = "完整"
)

// MeasurementQuality 是一轮测量的质量汇总。字段都是纯数据，测试可直接断言，
// 无需解析渲染后的文本（可测性做进结构里）。
type MeasurementQuality struct {
	SampleCount      int      // 样本量（配对数）
	Significant      bool     // 原始显著性判定，按入参原样记录，不被样本不足覆盖
	IncompleteReason string   // 不完整原因分类：样本不足 / 差异不显著 / 完整
	Warnings         []string // 可解释的监控盲区；"完整"时为空切片
}

// AssessMeasurementQuality 依据样本量与显著性给出本轮测量质量。
// 判定优先级与 compareIncompleteReason 一致：样本不足优先于显著性，
// 避免"显著但样本少"的假信号掩盖监控盲区。
//
// 该函数是纯函数：n 与 significant 按值传入不被修改；返回的 Warnings
// 是新分配的切片，不与任何调用方或全局状态共享，多次调用结果逐字节一致。
func AssessMeasurementQuality(n int, significant bool) MeasurementQuality {
	q := MeasurementQuality{
		SampleCount: n,
		Significant: significant,
		Warnings:    make([]string, 0, 1),
	}
	switch {
	case n < minComparePairs:
		q.IncompleteReason = qualityReasonInsufficient
		q.Warnings = append(q.Warnings, incompleteInsufficient)
	case !significant:
		q.IncompleteReason = qualityReasonNotSignificant
		q.Warnings = append(q.Warnings, incompleteNotSignificant)
	default:
		q.IncompleteReason = qualityReasonComplete
	}
	return q
}
