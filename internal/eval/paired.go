package eval

import (
	"errors"
	"fmt"
	"math"
)

// 本文件提供配对差值（paired delta）统计工具，借鉴 ratchet eval 的做法：
// 同一批场景在基线与候选两个版本上各跑一次，按场景配对求差，再用 t 分布
// 给出差值均值的 95% 置信区间（CI）。这样可以把"真实提升"和"噪声波动"
// 区分开：当 CI 不跨过 0 时，差异在 95% 置信水平上显著。
//
// 这里的函数都是纯函数：不修改输入，不依赖全局可变状态、时钟或网络。
// 它们独立于现有 golden 快照比对，不改变任何评分行为。

// ErrInsufficientSamples 是配对分析样本不足时返回的哨兵错误，
// 调用方可用 errors.Is 匹配。
var ErrInsufficientSamples = errors.New("eval: paired delta needs at least 2 sample pairs")

// Delta 是一次配对分析的汇总结果。
type Delta struct {
	// N 是有效配对数（两组各自的样本数，必须相等）。
	N int
	// BaselineMean 与 CandidateMean 分别是基线与候选的样本均值。
	BaselineMean  float64
	CandidateMean float64
	// MeanDiff 是配对差值的均值，恒等于 CandidateMean - BaselineMean。
	// 正值代表候选更好（前提是指标越大越好）。
	MeanDiff float64
	// StdDev 是配对差值的样本标准差（贝塞尔校正，除以 N-1）。
	StdDev float64
	// StdErr 是差值均值的标准误：StdDev / sqrt(N)。
	StdErr float64
	// CriticalT 是自由度 N-1、双尾 95% 的 t 临界值。
	CriticalT float64
	// CILow 与 CIHigh 是差值均值的 95% 置信区间端点：
	// MeanDiff ± CriticalT * StdErr。区间可能跨过 0。
	CILow  float64
	CIHigh float64
}

// PairedDelta 对两组等长、按场景一一对应的指标做配对分析。
// baseline[i] 与 candidate[i] 是同一场景在两个版本上的取值。
// 返回差值均值、两组的样本均值，以及差值均值的 95% 置信区间。
//
// 约束：
//   - 两组长度必须相等且至少为 2（CI 需要估计方差）；否则返回 ErrInsufficientSamples。
//   - 任何非有限值（NaN / ±Inf）都返回错误，因为这类值会污染方差与 CI。
//
// 该函数是纯函数，绝不修改传入的切片。
func PairedDelta(baseline, candidate []float64) (Delta, error) {
	n := len(baseline)
	if n != len(candidate) || n < 2 {
		return Delta{}, fmt.Errorf("%w: got %d and %d samples", ErrInsufficientSamples, len(baseline), len(candidate))
	}

	diffs := make([]float64, n)
	var baseSum, candSum, diffSum float64
	for i := 0; i < n; i++ {
		b, c := baseline[i], candidate[i]
		if !finite(b) || !finite(c) {
			return Delta{}, fmt.Errorf("eval: non-finite metric at pair %d (baseline=%v, candidate=%v)", i, b, c)
		}
		baseSum += b
		candSum += c
		diffs[i] = c - b
		diffSum += diffs[i]
	}

	meanDiff := diffSum / float64(n)
	// 样本方差衡量配对差值围绕其均值的离散程度（贝塞尔校正）。
	var variance float64
	for _, d := range diffs {
		delta := d - meanDiff
		variance += delta * delta
	}
	variance /= float64(n - 1)
	stdDev := math.Sqrt(variance)
	stdErr := stdDev / math.Sqrt(float64(n))
	critical := tCritical95(n - 1)
	margin := critical * stdErr

	return Delta{
		N:             n,
		BaselineMean:  baseSum / float64(n),
		CandidateMean: candSum / float64(n),
		MeanDiff:      meanDiff,
		StdDev:        stdDev,
		StdErr:        stdErr,
		CriticalT:     critical,
		CILow:         meanDiff - margin,
		CIHigh:        meanDiff + margin,
	}, nil
}

// finite 报告 f 是否为有限值（既非 NaN 也非 ±Inf）。
func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

// normal95 是自由度趋于无穷时双尾 95% 的正态近似临界值。
const normal95 = 1.959964

// tTable95 是 t 分布双尾 95% 临界值表：自由度 1..30 逐项列出，
// 之后是若干常用锚点，供更大自由度线性插值使用。数值取自标准 t 分布表。
var tTable95 = []struct {
	df int
	t  float64
}{
	{1, 12.706205}, {2, 4.302653}, {3, 3.182446}, {4, 2.776445},
	{5, 2.570582}, {6, 2.446912}, {7, 2.364624}, {8, 2.306004},
	{9, 2.262157}, {10, 2.228139}, {11, 2.200985}, {12, 2.178813},
	{13, 2.160369}, {14, 2.144787}, {15, 2.131450}, {16, 2.119905},
	{17, 2.109816}, {18, 2.100922}, {19, 2.093024}, {20, 2.085963},
	{21, 2.079614}, {22, 2.073873}, {23, 2.068658}, {24, 2.063899},
	{25, 2.059539}, {26, 2.055529}, {27, 2.051831}, {28, 2.048407},
	{29, 2.045230}, {30, 2.042272},
	{40, 2.021075}, {60, 2.000298}, {80, 1.990063},
	{100, 1.983972}, {120, 1.979930}, {1000, 1.962339},
}

// tCritical95 返回自由度 df 的双尾 95% t 临界值。
// df <= 30 直接查表；更大的自由度在相邻锚点间线性插值，
// 超过最大锚点后收敛到正态近似 normal95。df <= 0 时返回 NaN。
func tCritical95(df int) float64 {
	if df <= 0 {
		return math.NaN()
	}
	table := tTable95
	if df <= table[0].df {
		return table[0].t
	}
	for i := 1; i < len(table); i++ {
		if table[i].df == df {
			return table[i].t
		}
		if table[i].df > df {
			lo, hi := table[i-1], table[i]
			frac := float64(df-lo.df) / float64(hi.df-lo.df)
			return lo.t + frac*(hi.t-lo.t)
		}
	}
	return normal95
}
