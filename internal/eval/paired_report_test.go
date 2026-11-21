package eval

import (
	"reflect"
	"strings"
	"testing"
)

// TestClassifyDelta 覆盖正向、负向、不显著（跨零与恰好为零）以及 epsilon 容差边界。
// 期望的 Direction / Significant 以常量与布尔值精确断言，Detail 仅断言为稳定非空中文。
func TestClassifyDelta(t *testing.T) {
	tests := []struct {
		name       string
		delta      Delta
		epsilon    float64
		wantSig    bool
		wantDir    string
		wantDetail string // Detail 中必须出现的稳定子串
	}{
		{
			name:       "positive when lower bound above zero",
			delta:      Delta{CILow: 0.3, CIHigh: 0.7},
			wantSig:    true,
			wantDir:    verdictDirectionPositive,
			wantDetail: "显著正向",
		},
		{
			name:       "negative when upper bound below zero",
			delta:      Delta{CILow: -0.7, CIHigh: -0.3},
			wantSig:    true,
			wantDir:    verdictDirectionNegative,
			wantDetail: "显著负向",
		},
		{
			name:       "not significant when interval straddles zero",
			delta:      Delta{CILow: -0.2, CIHigh: 0.4},
			wantSig:    false,
			wantDir:    verdictDirectionNone,
			wantDetail: "不显著",
		},
		{
			name:       "not significant when lower bound is exactly zero",
			delta:      Delta{CILow: 0, CIHigh: 0.5},
			wantSig:    false,
			wantDir:    verdictDirectionNone,
			wantDetail: "不显著",
		},
		{
			name:       "not significant when upper bound is exactly zero",
			delta:      Delta{CILow: -0.5, CIHigh: 0},
			wantSig:    false,
			wantDir:    verdictDirectionNone,
			wantDetail: "不显著",
		},
		{
			name:       "strict greater than: lower bound equals epsilon is not significant",
			delta:      Delta{CILow: 0.01, CIHigh: 0.05},
			epsilon:    0.01,
			wantSig:    false,
			wantDir:    verdictDirectionNone,
			wantDetail: "不显著",
		},
		{
			name:       "strict less than: upper bound equals minus epsilon is not significant",
			delta:      Delta{CILow: -0.05, CIHigh: -0.01},
			epsilon:    0.01,
			wantSig:    false,
			wantDir:    verdictDirectionNone,
			wantDetail: "不显著",
		},
		{
			name:       "epsilon absorbs a small positive lower bound",
			delta:      Delta{CILow: 0.005, CIHigh: 0.02},
			epsilon:    0.01,
			wantSig:    false,
			wantDir:    verdictDirectionNone,
			wantDetail: "不显著",
		},
		{
			name:       "epsilon absorbs a small negative upper bound",
			delta:      Delta{CILow: -0.02, CIHigh: -0.005},
			epsilon:    0.01,
			wantSig:    false,
			wantDir:    verdictDirectionNone,
			wantDetail: "不显著",
		},
		{
			name:       "positive beyond epsilon",
			delta:      Delta{CILow: 0.02, CIHigh: 0.09},
			epsilon:    0.01,
			wantSig:    true,
			wantDir:    verdictDirectionPositive,
			wantDetail: "显著正向",
		},
		{
			name:       "negative beyond epsilon",
			delta:      Delta{CILow: -0.09, CIHigh: -0.02},
			epsilon:    0.01,
			wantSig:    true,
			wantDir:    verdictDirectionNegative,
			wantDetail: "显著负向",
		},
		{
			name:       "negative epsilon clamped to zero",
			delta:      Delta{CILow: 0.001, CIHigh: 0.002},
			epsilon:    -5,
			wantSig:    true,
			wantDir:    verdictDirectionPositive,
			wantDetail: "显著正向",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyDelta(tc.delta, tc.epsilon)
			if got.Significant != tc.wantSig {
				t.Fatalf("Significant = %t, want %t (Detail=%s)", got.Significant, tc.wantSig, got.Detail)
			}
			if got.Direction != tc.wantDir {
				t.Fatalf("Direction = %q, want %q", got.Direction, tc.wantDir)
			}
			if !strings.Contains(got.Detail, tc.wantDetail) {
				t.Fatalf("Detail = %q, want substring %q", got.Detail, tc.wantDetail)
			}
			if strings.TrimSpace(got.Detail) == "" {
				t.Fatal("Detail is empty")
			}
			// Detail 必须是稳定确定值：相同输入重复调用得到相同中文说明。
			if again := ClassifyDelta(tc.delta, tc.epsilon); !reflect.DeepEqual(got, again) {
				t.Fatalf("ClassifyDelta not deterministic: %+v vs %+v", got, again)
			}
		})
	}
}

// TestClassifyDeltaDoesNotMutateInput 确认 ClassifyDelta 是纯函数：
// Delta 按值传入且无引用成员，调用前后入参必须逐字段相等。
func TestClassifyDeltaDoesNotMutateInput(t *testing.T) {
	d := Delta{N: 5, MeanDiff: 0.2, CILow: 0.1, CIHigh: 0.3}
	before := d
	_ = ClassifyDelta(d, 0.01)
	if !reflect.DeepEqual(d, before) {
		t.Fatalf("ClassifyDelta mutated its input: got %+v, want %+v", d, before)
	}
}

// TestFormatDeltaReportStableText 用固定入参与中文场景名锁定报告格式与内容。
// 精确断言整段文本，既保证稳定性，也确认名称、均值差、95%CI 与 verdict 全部出现。
func TestFormatDeltaReportStableText(t *testing.T) {
	before := Delta{MeanDiff: 0.1, CILow: -0.05, CIHigh: 0.25}
	after := Delta{MeanDiff: 0.5, CILow: 0.3, CIHigh: 0.7}

	want := strings.Join([]string{
		"配对诊断报告: 中文场景名",
		"before: 均值差=+0.100000, 95%CI=[-0.050000, +0.250000]",
		"after:  均值差=+0.500000, 95%CI=[+0.300000, +0.700000]",
		"判定: 显著=true, 方向=正向",
		"依据: 显著正向：95%置信下界 +0.300000 > 阈值 +0.000000，候选优于基线",
		"",
	}, "\n")

	if got := FormatDeltaReport("中文场景名", before, after); got != want {
		t.Fatalf("FormatDeltaReport() mismatch\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// TestFormatDeltaReportVerdictVariants 确认报告中的判定行随 after 的 CI 正确变化，
// 覆盖正向、负向与不显著三种情形，且中文字段名出现在输出中。
func TestFormatDeltaReportVerdictVariants(t *testing.T) {
	base := Delta{MeanDiff: 1, CILow: 0.5, CIHigh: 1.5}
	tests := []struct {
		name       string
		after      Delta
		wantLine   string
		wantDetail string
	}{
		{
			name:       "positive",
			after:      Delta{MeanDiff: 0.4, CILow: 0.2, CIHigh: 0.6},
			wantLine:   "判定: 显著=true, 方向=正向",
			wantDetail: "显著正向",
		},
		{
			name:       "negative",
			after:      Delta{MeanDiff: -0.4, CILow: -0.6, CIHigh: -0.2},
			wantLine:   "判定: 显著=true, 方向=负向",
			wantDetail: "显著负向",
		},
		{
			name:       "not significant",
			after:      Delta{MeanDiff: 0.1, CILow: -0.2, CIHigh: 0.4},
			wantLine:   "判定: 显著=false, 方向=无",
			wantDetail: "不显著",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := FormatDeltaReport("case", base, tc.after)
			if !strings.Contains(got, tc.wantLine) {
				t.Fatalf("report missing verdict line %q:\n%s", tc.wantLine, got)
			}
			if !strings.Contains(got, tc.wantDetail) {
				t.Fatalf("report missing detail %q:\n%s", tc.wantDetail, got)
			}
			for _, field := range []string{"配对诊断报告", "均值差", "95%CI", "判定", "依据"} {
				if !strings.Contains(got, field) {
					t.Fatalf("report missing field %q:\n%s", field, got)
				}
			}
		})
	}
}

// TestFormatDeltaReportDeterministic 确认同一组入参重复调用得到完全相同的文本。
func TestFormatDeltaReportDeterministic(t *testing.T) {
	before := Delta{N: 4, MeanDiff: -0.1, CILow: -0.3, CIHigh: 0.1}
	after := Delta{N: 4, MeanDiff: 0.25, CILow: 0.05, CIHigh: 0.45}
	first := FormatDeltaReport("确定性", before, after)
	for i := 0; i < 5; i++ {
		if got := FormatDeltaReport("确定性", before, after); got != first {
			t.Fatalf("FormatDeltaReport not deterministic on call %d\ngot:\n%s\nwant:\n%s", i, got, first)
		}
	}
}

// TestFormatDeltaReportDoesNotMutateInputs 确认 FormatDeltaReport 是纯函数。
func TestFormatDeltaReportDoesNotMutateInputs(t *testing.T) {
	before := Delta{N: 3, MeanDiff: 0.2, CILow: 0.0, CIHigh: 0.4}
	after := Delta{N: 3, MeanDiff: 0.6, CILow: 0.3, CIHigh: 0.9}
	beforeCopy, afterCopy := before, after
	_ = FormatDeltaReport("纯函数", before, after)
	if !reflect.DeepEqual(before, beforeCopy) || !reflect.DeepEqual(after, afterCopy) {
		t.Fatalf("FormatDeltaReport mutated inputs: before=%+v after=%+v", before, after)
	}
}
