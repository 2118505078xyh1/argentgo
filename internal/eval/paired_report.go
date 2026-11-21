package eval

import (
	"fmt"
	"strings"
)

// 本文件提供配对差值的诊断导出原语：把 Delta 分类成稳定的中文结论，
// 并把两个 Delta（before/after）渲染成稳定、可快照的文本报告。
//
// 与 paired.go / ratchet_optimizer.go 一样，这里的函数都是纯函数：
// 不修改入参、不读时钟/网络/任何全局可变状态，也不触碰现有 golden/评分路径。
// 目前没有调用方，属于独立的导出原语。

// DeltaVerdict 是对一个 Delta 的显著性判定结果。
// Direction 与 Detail 都是稳定的中文字符串，可直接写入报告或审计日志。
type DeltaVerdict struct {
	// Significant 表示差值在容忍度之外是否显著不为零。
	Significant bool
	// Direction 是稳定的中文方向标记："正向"、"负向" 或 "无"。
	Direction string
	// Detail 是带数值的稳定中文说明。
	Detail string
}

// 稳定的中文方向标记。用常量而非散落的字面量，保证输出跨版本一致。
const (
	verdictDirectionPositive = "正向"
	verdictDirectionNegative = "负向"
	verdictDirectionNone     = "无"
)

// ClassifyDelta 依据差值的 95% 置信区间判断显著性。
//
// 判定规则（epsilon 为边界容差，负值按 0 处理）：
//   - CILow > epsilon：显著正向（候选优于基线）；
//   - CIHigh < -epsilon：显著负向（候选劣于基线）；
//   - 其余（区间跨零、端点落在 [-epsilon, epsilon] 容差带内，或恰好等于阈值）：不显著。
//
// epsilon=0 时退化为基本规则 CILow>0 / CIHigh<0。epsilon 只放宽"不显著"
// 的区间，不改变正负方向的含义。Delta 按值传入且不被修改。
func ClassifyDelta(d Delta, epsilon float64) DeltaVerdict {
	if epsilon < 0 {
		epsilon = 0
	}
	switch {
	case d.CILow > epsilon:
		return DeltaVerdict{
			Significant: true,
			Direction:   verdictDirectionPositive,
			Detail: fmt.Sprintf(
				"显著正向：95%%置信下界 %+.6f > 阈值 %+.6f，候选优于基线",
				d.CILow, epsilon),
		}
	case d.CIHigh < -epsilon:
		return DeltaVerdict{
			Significant: true,
			Direction:   verdictDirectionNegative,
			Detail: fmt.Sprintf(
				"显著负向：95%%置信上界 %+.6f < 阈值 %+.6f，候选劣于基线",
				d.CIHigh, -epsilon),
		}
	default:
		return DeltaVerdict{
			Significant: false,
			Direction:   verdictDirectionNone,
			Detail: fmt.Sprintf(
				"不显著：95%%置信区间 [%+.6f, %+.6f] 落在 ±%.6f 容差内或跨零，无法区分真实差异与噪声",
				d.CILow, d.CIHigh, epsilon),
		}
	}
}

// FormatDeltaReport 把一次 before/after 对比渲染为稳定的中文文本报告。
// 报告依次包含：名称、before 与 after 各自的均值差与 95%CI、以及对 after 的判定。
// 判定使用 ClassifyDelta(after, 0)，即不带额外容差的基本规则；before 仅作对照背景。
//
// 输出只依赖入参，格式固定，适合直接做快照比对；两个 Delta 按值传入，
// 函数不会修改它们。
func FormatDeltaReport(name string, before, after Delta) string {
	verdict := ClassifyDelta(after, 0)
	var b strings.Builder
	fmt.Fprintf(&b, "配对诊断报告: %s\n", name)
	fmt.Fprintf(&b, "before: 均值差=%+.6f, 95%%CI=[%+.6f, %+.6f]\n",
		before.MeanDiff, before.CILow, before.CIHigh)
	fmt.Fprintf(&b, "after:  均值差=%+.6f, 95%%CI=[%+.6f, %+.6f]\n",
		after.MeanDiff, after.CILow, after.CIHigh)
	fmt.Fprintf(&b, "判定: 显著=%t, 方向=%s\n", verdict.Significant, verdict.Direction)
	fmt.Fprintf(&b, "依据: %s\n", verdict.Detail)
	return b.String()
}
