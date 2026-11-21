package eval

import (
	"fmt"
)

// 本文件给 CompareResults 补一行 token 经济学诊断，填上"只比 0/1 通过率"的盲区：
// 完成率提升若伴随 token 翻倍，在单价不变的前提下成本其实上升，不能被当作
// "免费提升"。这里只产出 aside 诊断文本，不参与 verdict 分类、通过门或 BestScore。
//
// 与 paired.go / paired_report.go 一致：纯函数，不修改入参、不读时钟/网络/全局状态。

// tokenEconomyPrefix 是诊断行的稳定前缀，便于快照比对与监控检索。
const tokenEconomyPrefix = "token: "

// tokenEconomyInsufficient 是配对数不足（<2）时的保守说明。
// 单个配对无法估计差值方差，因此不伪造均值与置信区间。
const tokenEconomyInsufficient = tokenEconomyPrefix + "样本不足，跳过 token 经济学诊断（配对数 n=%d，需至少 2 对）"

// tokenEconomyNoData 是两臂 token 合计均为 0（脚本化 runner 未上报 usage）时的保守说明。
// 全零并不代表"没有变化"，而是根本没有可比数据，不能输出误导性的"基线 0 → 候选 0"。
const tokenEconomyNoData = tokenEconomyPrefix + "两臂 token 用量均为 0，无可比数据，跳过 token 经济学诊断"

// tokenEconomyMismatch 是两臂向量长度不等（无法逐场景配对）时的保守说明。
const tokenEconomyMismatch = tokenEconomyPrefix + "两臂向量长度不等，无可比数据，跳过 token 经济学诊断（基线 n=%d，候选 n=%d）"

// TokenEconomyDiagnosis 对基线与候选两臂逐场景的 Usage.TotalTokens 做配对分析，
// 返回一行稳定的中文诊断（不含行尾换行，由调用方决定如何拼接）。
//
// 入参按场景一一对应（baseline[i] 与 candidate[i] 是同一场景在两个版本上的结果）。
// 处理流程：
//  1. 长度不等返回"无可比数据"保守说明，配对数 <2 返回"样本不足"保守说明，均不伪造统计数字；
//  2. 两臂 token 合计均为 0：说明 runner 未上报用量，返回"无可比数据"保守说明；
//  3. 否则把每个 TotalTokens（int）转成 float64，复用 PairedDelta 求配对差与 95%CI；
//  4. 用 finiteOrZero 钳制所有输出数字，保证行内绝不出现 NaN / ±Inf；
//  5. 渲染为单行："基线均值 → 候选均值（Δ差值，95%CI=[下界, 上界]，n=配对数）"。
//
// 该函数是纯函数：不修改入参、不读时钟/网络/文件/任何全局可变状态。
// 它只产出诊断文本，不改变任何 verdict 文本，也不并入通过判定或 BestScore。
func TokenEconomyDiagnosis(baseline, candidate []Result) string {
	n := len(baseline)
	if n != len(candidate) {
		return fmt.Sprintf(tokenEconomyMismatch, n, len(candidate))
	}
	if n < 2 {
		return fmt.Sprintf(tokenEconomyInsufficient, n)
	}

	baseTokens := make([]float64, n)
	candTokens := make([]float64, n)
	var baseTotal, candTotal int64
	for i := range baseline {
		b := int64(baseline[i].Usage.TotalTokens())
		c := int64(candidate[i].Usage.TotalTokens())
		baseTokens[i] = float64(b)
		candTokens[i] = float64(c)
		baseTotal += b
		candTotal += c
	}
	if baseTotal == 0 && candTotal == 0 {
		return tokenEconomyNoData
	}

	delta, err := PairedDelta(baseTokens, candTokens)
	if err != nil {
		// TotalTokens 是整数，理论上不会触发非有限错误；防御性返回保守说明。
		return fmt.Sprintf(tokenEconomyInsufficient, n)
	}

	return fmt.Sprintf(
		tokenEconomyPrefix+"基线 %.1f → 候选 %.1f（Δ%+.1f，95%%CI=[%+.1f, %+.1f]，n=%d）",
		finiteOrZero(delta.BaselineMean),
		finiteOrZero(delta.CandidateMean),
		finiteOrZero(delta.MeanDiff),
		finiteOrZero(delta.CILow),
		finiteOrZero(delta.CIHigh),
		delta.N,
	)
}

// finiteOrZero 把非有限值（NaN / ±Inf）钳制为 0，保证诊断行绝不出现 "NaN"。
// finite 定义在 paired.go，同包直接复用。
func finiteOrZero(f float64) float64 {
	if !finite(f) {
		return 0
	}
	return f
}
