package eval

import (
	"fmt"
	"strings"
)

// 本文件把 CompareResults 的"诊断段"做成结构化、纯函数的原语：
// 先由 BuildCompareDiagnostic 产出可断言的 CompareDiagnostic（把可测性做进结构里），
// 再由 Finalize 依据显著性标注 incomplete-reason 分类，最后由 FormatCompareDiagnostic
// 渲染成稳定的中文文本。incomplete-reason 把"样本不足导致无法下更优结论"这一
// 监控盲区显式分类，使监控侧能区分"样本不足"与"样本充足但不显著"。
//
// 与 paired.go / paired_report.go 一致，这里不修改入参、不读时钟/网络/全局状态，
// 也不触碰 golden/评分路径。

// 稳定的翻转方向标记。用常量而非散落字面量，保证输出跨版本一致。
const (
	// flipGain 表示基线失败、候选通过：候选修好了该场景（0→1）。
	flipGain = "失败→通过"
	// flipLoss 表示基线通过、候选失败：候选弄坏了该场景（1→0）。
	flipLoss = "通过→失败"
)

// 稳定的样本不足守卫状态文本。
const (
	guardTriggered = "已触发"
	guardClear     = "未触发"
)

// 稳定的不完整原因分类。把"为什么不能给出更优结论"显式分类，
// 让监控侧看见诊断盲区，而不是只看一个笼统的 verdict。
const (
	incompleteNone           = ""                     // 无盲区：样本充足且差异显著
	incompleteInsufficient   = "样本不足（n < 30），不下更优结论"  // n < minComparePairs，守卫触发
	incompleteNotSignificant = "差异不显著（置信区间跨零），不下更优结论" // 样本充足但 CI 跨零
)

// CompareFlip 是单个场景在基线与候选之间的 0/1 通过状态翻转明细。
type CompareFlip struct {
	Index     int    // 场景下标（0 基）
	Name      string // 场景名，取自对应的 Result.Name
	Baseline  bool   // 基线是否通过
	Candidate bool   // 候选是否通过
	Kind      string // flipGain 或 flipLoss
}

// CompareDiagnostic 是诊断段的结构化结果。字段都是纯数据，
// 测试可直接断言，无需解析渲染后的文本（可测性做进结构里）。
type CompareDiagnostic struct {
	N                int           // 样本量（配对数）
	Insufficient     bool          // 是否触发样本不足守卫（N < GuardThreshold）
	GuardThreshold   int           // 触发守卫的阈值
	FlipCount        int           // 翻转场景总数
	GainCount        int           // 失败→通过的数量
	LossCount        int           // 通过→失败的数量
	Flips            []CompareFlip // 按场景下标升序，保证确定性
	IncompleteReason string        // 不完整原因分类；空串表示无盲区
}

// BuildCompareDiagnostic 从基线与候选向量构造诊断段的结构化数据。
// 只依赖两份向量的 Passed/Name 字段；Flips 按场景下标升序排列，保证确定性。
// 此时 IncompleteReason 留空，需由 Finalize 在显著性已知后补全。
// 该函数是纯函数，不修改入参，也不共享任何可变状态。
func BuildCompareDiagnostic(baseline, candidate []Result) CompareDiagnostic {
	d := CompareDiagnostic{
		N:              len(baseline),
		Insufficient:   len(baseline) < minComparePairs,
		GuardThreshold: minComparePairs,
	}
	flips := make([]CompareFlip, 0)
	for i := range baseline {
		from, to := baseline[i].Passed, candidate[i].Passed
		if from == to {
			continue
		}
		kind := flipGain
		if from && !to {
			kind = flipLoss
		}
		flips = append(flips, CompareFlip{
			Index:     i,
			Name:      baseline[i].Name,
			Baseline:  from,
			Candidate: to,
			Kind:      kind,
		})
		if kind == flipGain {
			d.GainCount++
		} else {
			d.LossCount++
		}
	}
	d.Flips = flips
	d.FlipCount = len(flips)
	return d
}

// Finalize 依据显著性补全诊断段的不完整原因分类，返回新的值（不改原值）。
// significant 应取自 ClassifyDelta 的 Significant，且样本不足时无论该值如何都判为"样本不足"。
func (d CompareDiagnostic) Finalize(significant bool) CompareDiagnostic {
	d.IncompleteReason = compareIncompleteReason(d.N, significant)
	return d
}

// compareIncompleteReason 依据样本量与显著性返回不完整原因分类。
// 样本不足的优先级最高：即使原始 Delta 显著，也不掩盖监控盲区。
func compareIncompleteReason(n int, significant bool) string {
	switch {
	case n < minComparePairs:
		return incompleteInsufficient
	case !significant:
		return incompleteNotSignificant
	default:
		return incompleteNone
	}
}

// FormatCompareDiagnostic 把诊断段渲染为稳定的中文文本，适合快照比对。
// 包含样本量、样本不足守卫状态、翻转计数、逐场景 0/1 翻转明细与不完整原因。
func FormatCompareDiagnostic(d CompareDiagnostic) string {
	var b strings.Builder
	fmt.Fprintf(&b, "诊断段: %s\n", compareReportName)
	fmt.Fprintf(&b, "样本量 n=%d\n", d.N)

	guard := guardClear
	if d.Insufficient {
		guard = guardTriggered
	}
	fmt.Fprintf(&b, "样本不足守卫: %s（n=%d 与阈值 %d 比较）\n",
		guard, d.N, d.GuardThreshold)

	fmt.Fprintf(&b, "翻转数: %d（%s %d，%s %d）\n",
		d.FlipCount, flipGain, d.GainCount, flipLoss, d.LossCount)
	if len(d.Flips) == 0 {
		fmt.Fprintf(&b, "翻转明细: 无\n")
	} else {
		fmt.Fprintf(&b, "翻转明细:\n")
		for _, f := range d.Flips {
			fmt.Fprintf(&b, "  - 场景 %d %q: %s (%d→%d)\n",
				f.Index+1, displayCompareName(f.Name), f.Kind,
				boolToInt(f.Baseline), boolToInt(f.Candidate))
		}
	}

	reason := d.IncompleteReason
	if reason == "" {
		reason = "完整：样本充足且差异显著"
	}
	fmt.Fprintf(&b, "不完整原因: %s\n", reason)
	return b.String()
}

// displayCompareName 为空场景名提供稳定的占位文本，避免明细行难以辨认。
func displayCompareName(name string) string {
	if name == "" {
		return "（未命名）"
	}
	return name
}
