package eval

import (
	"errors"
	"fmt"
	"strings"
)

// 本文件把两份已评分的 Result 向量接到已有的配对统计原语上，给出稳定的中文 verdict。
// 它是 R31 eval 闭环的接线点：PairedDelta / ClassifyDelta / FormatDeltaReport 此前
// 都只有原语没有调用方，这里把它们串成一条可供 CLI 直接使用的纯函数路径。
//
// 重要：完成率基准只有 6 个场景（n=6）。配对 t 检验在如此小的样本上几乎永不显著，
// 单个场景翻转在统计上毫无意义。因此本文件对 n < minComparePairs 一律输出
// "样本不足 / 不显著"，并明确禁止据此给出"候选更优（提升）"的结论，
// 即使原始置信区间偶然跨过了显著性阈值。

// minComparePairs 是给出"显著提升 / 显著退化"结论所需的最小配对数。
// 低于该阈值时，配对 t 检验的自由度过小，结论不具统计意义，
// 一律降级为"样本不足"。30 是常用的 t 检验经验下限。
const minComparePairs = 30

// ErrCompareVectors 表示两份待比较的 Result 向量长度不合法：
// 配对分析要求两者等长、非空且按场景一一对应。
var ErrCompareVectors = errors.New("eval: compare requires two non-empty equal-length result vectors")

// compareReportName 是传给 FormatDeltaReport 的稳定中文名称，
// 锁定报告首行文本，便于快照比对。
const compareReportName = "完成率对比"

// passedMetric 把一个 Result 的通过与否映射为配对统计所需的 0/1 指标。
// 1 表示通过，0 表示未通过；指标越大越好，与 PairedDelta 的符号约定一致。
func passedMetric(r Result) float64 {
	if r.Passed {
		return 1
	}
	return 0
}

// CompareResults 比较基线与候选两份 Result 向量，返回稳定的中文 verdict 文本。
//
// 入参按场景一一对应（baseline[i] 与 candidate[i] 是同一场景在两个版本上的结果）。
// 处理流程：
//  1. 校验两份向量等长且非空，否则返回包装了 ErrCompareVectors 的错误；
//  2. 把每个 Result.Passed 映射为 0/1，调用 PairedDelta 得到差值均值与 95%CI；
//  3. 调用 ClassifyDelta 判定方向；
//  4. 用 FormatDeltaReport 渲染 before（无变化基线，恒为零）/ after（观测差值）报告；
//  5. 追加一行显式 verdict：
//     - n < minComparePairs：输出"样本不足 / 不显著"，禁止给出候选更优的结论；
//     - 样本充足但区间跨零：输出"不显著"，同样不下结论；
//     - 显著正向 / 显著负向：分别输出"显著提升 / 显著退化"。
//
// 该函数是纯函数：不修改入参、不读时钟/网络/文件/任何全局可变状态。
// 样本不足时即便原始 Delta 被 ClassifyDelta 判为显著，verdict 也一律降级，
// 否则 n=6 的单翻转会被误读成"有效提升"。
func CompareResults(baseline, candidate []Result) (string, error) {
	n := len(baseline)
	if n == 0 || n != len(candidate) {
		return "", fmt.Errorf("%w: got %d and %d results", ErrCompareVectors, len(baseline), len(candidate))
	}

	// 配对统计至少需要两个样本才能估计差值方差。n<2 时无法计算 CI，
	// 直接给出"样本不足"结论，不伪造统计数字。
	if n < 2 {
		return insufficientCompareVerdict(n), nil
	}

	baseMetrics := make([]float64, n)
	candMetrics := make([]float64, n)
	for i := range baseline {
		baseMetrics[i] = passedMetric(baseline[i])
		candMetrics[i] = passedMetric(candidate[i])
	}

	delta, err := PairedDelta(baseMetrics, candMetrics)
	if err != nil {
		return "", err
	}
	verdict := ClassifyDelta(delta, 0)

	// before 是"无变化"基线（配对差值恒为零），after 是观测到的候选差值。
	// 把两者交给 FormatDeltaReport，复用其稳定的中文报告格式。
	before := Delta{N: delta.N}
	var b strings.Builder
	b.WriteString(FormatDeltaReport(compareReportName, before, delta))

	switch {
	case delta.N < minComparePairs:
		// 样本不足：必须出现在 verdict 中，且绝不输出"提升/显著正向"这类结论。
		b.WriteString(insufficientCompareVerdict(delta.N))
	case !verdict.Significant:
		fmt.Fprintf(&b, "verdict: 差异不显著：完成率差值区间的置信区间跨零，不能判定候选更优\n")
	case verdict.Direction == verdictDirectionPositive:
		fmt.Fprintf(&b, "verdict: 显著提升：候选完成率更高\n")
	default:
		fmt.Fprintf(&b, "verdict: 显著退化：候选完成率更低\n")
	}
	return b.String(), nil
}

// insufficientCompareVerdict 返回样本不足时的单行 verdict。
// 文本刻意包含"样本不足"与"不显著"两个稳定子串，且不含"提升"/"显著正向"，
// 使 n=6 的单翻转无法被误读成有效提升。
func insufficientCompareVerdict(n int) string {
	return fmt.Sprintf(
		"verdict: 样本不足（n=%d < %d）：完成率差异不显著，不能判定候选更优\n",
		n, minComparePairs)
}
