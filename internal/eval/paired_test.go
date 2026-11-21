package eval

import (
	"errors"
	"math"
	"reflect"
	"testing"
)

// approxEqual 以绝对误差 1e-5 比较两个浮点数，避免 CI 端点的
// 末位误差导致脆弱的失败。
func approxEqual(t *testing.T, label string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-5 {
		t.Fatalf("%s = %.9f, want %.9f", label, got, want)
	}
}

// TestPairedDeltaMeansAndDiff 验证两组样本均值与配对差值均值的计算。
// 差值恒为 candidate - baseline，正负号代表提升或退化。
func TestPairedDeltaMeansAndDiff(t *testing.T) {
	tests := []struct {
		name         string
		baseline     []float64
		candidate    []float64
		wantBaseMean float64
		wantCandMean float64
		wantMeanDiff float64
	}{
		{
			name:         "constant improvement",
			baseline:     []float64{10, 10, 10},
			candidate:    []float64{12, 12, 12},
			wantBaseMean: 10,
			wantCandMean: 12,
			wantMeanDiff: 2,
		},
		{
			name:         "sequential values",
			baseline:     []float64{1, 2, 3},
			candidate:    []float64{2, 4, 6},
			wantBaseMean: 2,
			wantCandMean: 4,
			wantMeanDiff: 2,
		},
		{
			name:         "mixed signs cancel out",
			baseline:     []float64{1, 2, 3},
			candidate:    []float64{3, 2, 1},
			wantBaseMean: 2,
			wantCandMean: 2,
			wantMeanDiff: 0,
		},
		{
			name:         "regression is negative",
			baseline:     []float64{5, 6, 7, 8},
			candidate:    []float64{4, 4, 4, 4},
			wantBaseMean: 6.5,
			wantCandMean: 4,
			wantMeanDiff: -2.5,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := PairedDelta(tc.baseline, tc.candidate)
			if err != nil {
				t.Fatalf("PairedDelta() unexpected error: %v", err)
			}
			if got.N != len(tc.baseline) {
				t.Fatalf("N = %d, want %d", got.N, len(tc.baseline))
			}
			approxEqual(t, "BaselineMean", got.BaselineMean, tc.wantBaseMean)
			approxEqual(t, "CandidateMean", got.CandidateMean, tc.wantCandMean)
			approxEqual(t, "MeanDiff", got.MeanDiff, tc.wantMeanDiff)
			approxEqual(t, "MeanDiff vs means", got.MeanDiff, got.CandidateMean-got.BaselineMean)
		})
	}
}

// TestPairedDeltaConfidenceInterval 验证标准误、t 临界值与 95% CI 端点。
// 期望值由独立的公式与已知 t 表值推导，而非复用被测实现。
func TestPairedDeltaConfidenceInterval(t *testing.T) {
	tests := []struct {
		name          string
		baseline      []float64
		candidate     []float64
		wantStdDev    float64
		wantStdErr    float64
		wantCriticalT float64
	}{
		{
			// df=1 的临界值极大，CI 很宽：反映只有两个配对时几乎无法下结论。
			name:          "two pairs have a very wide interval",
			baseline:      []float64{0, 0},
			candidate:     []float64{1, 3},
			wantStdDev:    math.Sqrt(2),
			wantStdErr:    1,
			wantCriticalT: 12.706205,
		},
		{
			name:          "three pairs",
			baseline:      []float64{0, 0, 0},
			candidate:     []float64{1, 2, 3},
			wantStdDev:    1,
			wantStdErr:    1 / math.Sqrt(3),
			wantCriticalT: 4.302653,
		},
		{
			// 零方差时 margin 为 0，CI 退化为单点 [mean, mean]。
			name:          "zero variance collapses the interval",
			baseline:      []float64{1, 2, 3, 4},
			candidate:     []float64{2, 3, 4, 5},
			wantStdDev:    0,
			wantStdErr:    0,
			wantCriticalT: 3.182446,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := PairedDelta(tc.baseline, tc.candidate)
			if err != nil {
				t.Fatalf("PairedDelta() unexpected error: %v", err)
			}
			approxEqual(t, "StdDev", got.StdDev, tc.wantStdDev)
			approxEqual(t, "StdErr", got.StdErr, tc.wantStdErr)
			approxEqual(t, "CriticalT", got.CriticalT, tc.wantCriticalT)

			margin := tc.wantCriticalT * tc.wantStdErr
			wantLow := got.MeanDiff - margin
			wantHigh := got.MeanDiff + margin
			approxEqual(t, "CILow", got.CILow, wantLow)
			approxEqual(t, "CIHigh", got.CIHigh, wantHigh)

			// CI 必须包含均值，且均值恰为两端点的中点。
			if got.CILow > got.MeanDiff || got.CIHigh < got.MeanDiff {
				t.Fatalf("CI [%.9f, %.9f] does not bracket mean %.9f", got.CILow, got.CIHigh, got.MeanDiff)
			}
			approxEqual(t, "CI midpoint", (got.CILow+got.CIHigh)/2, got.MeanDiff)
		})
	}
}

// TestPairedDeltaInsufficientSamples 覆盖样本不足与长度不匹配的错误路径，
// 并确认返回的哨兵错误可用 errors.Is 匹配。
func TestPairedDeltaInsufficientSamples(t *testing.T) {
	tests := []struct {
		name      string
		baseline  []float64
		candidate []float64
	}{
		{"nil slices", nil, nil},
		{"empty slices", []float64{}, []float64{}},
		{"single pair", []float64{1}, []float64{2}},
		{"length mismatch 2 vs 3", []float64{1, 2}, []float64{1, 2, 3}},
		{"empty baseline", []float64{}, []float64{1, 2}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := PairedDelta(tc.baseline, tc.candidate)
			if !errors.Is(err, ErrInsufficientSamples) {
				t.Fatalf("PairedDelta() error = %v, want ErrInsufficientSamples", err)
			}
			if !reflect.DeepEqual(got, Delta{}) {
				t.Fatalf("PairedDelta() on insufficient samples = %+v, want zero Delta", got)
			}
		})
	}
}

// TestPairedDeltaRejectsNonFinite 验证 NaN 与 ±Inf 会被拒绝，
// 且这类错误不是"样本不足"。
func TestPairedDeltaRejectsNonFinite(t *testing.T) {
	tests := []struct {
		name      string
		baseline  []float64
		candidate []float64
	}{
		{"NaN in baseline", []float64{1, math.NaN()}, []float64{2, 3}},
		{"NaN in candidate", []float64{1, 2}, []float64{2, math.NaN()}},
		{"positive infinity", []float64{1, 2}, []float64{2, math.Inf(1)}},
		{"negative infinity", []float64{1, math.Inf(-1)}, []float64{2, 3}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := PairedDelta(tc.baseline, tc.candidate)
			if err == nil {
				t.Fatal("PairedDelta() accepted non-finite input")
			}
			if errors.Is(err, ErrInsufficientSamples) {
				t.Fatalf("PairedDelta() classified non-finite input as insufficient samples: %v", err)
			}
			if !reflect.DeepEqual(got, Delta{}) {
				t.Fatalf("PairedDelta() returned non-zero Delta on error: %+v", got)
			}
		})
	}
}

// TestPairedDeltaDoesNotMutateInput 确认 PairedDelta 是纯函数。
func TestPairedDeltaDoesNotMutateInput(t *testing.T) {
	baseline := []float64{1, 2, 3, 4}
	candidate := []float64{2, 4, 6, 8}
	baseBefore := append([]float64(nil), baseline...)
	candBefore := append([]float64(nil), candidate...)
	if _, err := PairedDelta(baseline, candidate); err != nil {
		t.Fatalf("PairedDelta() unexpected error: %v", err)
	}
	if !reflect.DeepEqual(baseline, baseBefore) || !reflect.DeepEqual(candidate, candBefore) {
		t.Fatalf("PairedDelta() mutated its inputs: baseline=%v candidate=%v", baseline, candidate)
	}
}

// TestTCritical95 验证 t 临界值查表、插值与正态近似的边界。
func TestTCritical95(t *testing.T) {
	tests := []struct {
		name string
		df   int
		want float64
	}{
		{"df=1", 1, 12.706205},
		{"df=2", 2, 4.302653},
		{"df=10", 10, 2.228139},
		{"df=30 table edge", 30, 2.042272},
		{"df=35 interpolated between 30 and 40", 35, 2.031674},
		{"df=40 anchor", 40, 2.021075},
		{"df=100 anchor", 100, 1.983972},
		{"df=1000 anchor", 1000, 1.962339},
		{"df=2000 normal approximation", 2000, normal95},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			approxEqual(t, "tCritical95", tCritical95(tc.df), tc.want)
		})
	}
	if got := tCritical95(0); !math.IsNaN(got) {
		t.Fatalf("tCritical95(0) = %v, want NaN", got)
	}
	if got := tCritical95(-5); !math.IsNaN(got) {
		t.Fatalf("tCritical95(-5) = %v, want NaN", got)
	}
}
