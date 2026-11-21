package eval

import (
	"reflect"
	"testing"
	"unicode"
	"unicode/utf8"
)

// TestAssessMeasurementQualityMatrix 覆盖 n 边界（含负数、0、29/30/31 分水岭）
// 与 significant 组合，锁定原因分类、原始显著性记录与盲区告警。
func TestAssessMeasurementQualityMatrix(t *testing.T) {
	cases := []struct {
		name        string
		n           int
		significant bool
		wantReason  string
		wantWarns   []string
	}{
		{name: "负数样本", n: -1, significant: true, wantReason: qualityReasonInsufficient, wantWarns: []string{incompleteInsufficient}},
		{name: "n=0 样本不足", n: 0, significant: false, wantReason: qualityReasonInsufficient, wantWarns: []string{incompleteInsufficient}},
		{name: "n=1 样本不足", n: 1, significant: true, wantReason: qualityReasonInsufficient, wantWarns: []string{incompleteInsufficient}},
		{name: "n=29 样本不足但显著", n: 29, significant: true, wantReason: qualityReasonInsufficient, wantWarns: []string{incompleteInsufficient}},
		{name: "n=30 边界不显著", n: 30, significant: false, wantReason: qualityReasonNotSignificant, wantWarns: []string{incompleteNotSignificant}},
		{name: "n=30 边界显著", n: 30, significant: true, wantReason: qualityReasonComplete, wantWarns: []string{}},
		{name: "n=31 不显著", n: 31, significant: false, wantReason: qualityReasonNotSignificant, wantWarns: []string{incompleteNotSignificant}},
		{name: "n=31 显著", n: 31, significant: true, wantReason: qualityReasonComplete, wantWarns: []string{}},
		{name: "大样本显著", n: 1000, significant: true, wantReason: qualityReasonComplete, wantWarns: []string{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := AssessMeasurementQuality(tc.n, tc.significant)
			if got.SampleCount != tc.n {
				t.Errorf("SampleCount = %d, want %d", got.SampleCount, tc.n)
			}
			// 原始显著性必须按入参原样记录，哪怕样本不足覆盖了原因分类。
			if got.Significant != tc.significant {
				t.Errorf("Significant = %t, want %t (must record raw input)", got.Significant, tc.significant)
			}
			if got.IncompleteReason != tc.wantReason {
				t.Errorf("IncompleteReason = %q, want %q", got.IncompleteReason, tc.wantReason)
			}
			if !reflect.DeepEqual(got.Warnings, tc.wantWarns) {
				t.Errorf("Warnings = %#v, want %#v", got.Warnings, tc.wantWarns)
			}
		})
	}
}

// TestAssessMeasurementQualityReasonCategories 锁定三类原因标签的取值，
// 并验证"完整"只在样本充足且显著时出现，其余情形必有对应盲区告警。
func TestAssessMeasurementQualityReasonCategories(t *testing.T) {
	if qualityReasonInsufficient != "样本不足" ||
		qualityReasonNotSignificant != "差异不显著" ||
		qualityReasonComplete != "完整" {
		t.Fatalf("unexpected reason labels: %q / %q / %q",
			qualityReasonInsufficient, qualityReasonNotSignificant, qualityReasonComplete)
	}

	for _, n := range []int{0, 29, 30, 31} {
		for _, sig := range []bool{false, true} {
			q := AssessMeasurementQuality(n, sig)
			switch q.IncompleteReason {
			case qualityReasonInsufficient:
				if n >= minComparePairs {
					t.Errorf("n=%d sig=%t: 样本不足分类不应出现在充足样本上", n, sig)
				}
				if len(q.Warnings) == 0 {
					t.Errorf("n=%d sig=%t: 盲区分类必须有告警", n, sig)
				}
			case qualityReasonNotSignificant:
				if n < minComparePairs || sig {
					t.Errorf("n=%d sig=%t: 差异不显著分类条件不符", n, sig)
				}
				if len(q.Warnings) == 0 {
					t.Errorf("n=%d sig=%t: 盲区分类必须有告警", n, sig)
				}
			case qualityReasonComplete:
				if n < minComparePairs || !sig {
					t.Errorf("n=%d sig=%t: 完整分类条件不符", n, sig)
				}
				if len(q.Warnings) != 0 {
					t.Errorf("n=%d sig=%t: 完整分类不应有告警: %#v", n, sig, q.Warnings)
				}
			default:
				t.Errorf("n=%d sig=%t: 未知原因分类 %q", n, sig, q.IncompleteReason)
			}
		}
	}
}

// TestAssessMeasurementQualityCJK 原因分类与告警都应为合法的中文文本。
func TestAssessMeasurementQualityCJK(t *testing.T) {
	for _, tc := range []struct {
		n   int
		sig bool
	}{{5, false}, {29, true}, {30, false}, {30, true}} {
		q := AssessMeasurementQuality(tc.n, tc.sig)
		texts := append([]string{q.IncompleteReason}, q.Warnings...)
		for _, s := range texts {
			if !utf8.ValidString(s) {
				t.Errorf("n=%d sig=%t: invalid UTF-8: %q", tc.n, tc.sig, s)
			}
			if !hasHan(s) {
				t.Errorf("n=%d sig=%t: expected Chinese text, got %q", tc.n, tc.sig, s)
			}
		}
	}
}

// hasHan 报告字符串是否含有汉字，用于核对中文输出。
func hasHan(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

// TestAssessMeasurementQualityDeterminism 相同入参的多次调用必须逐字段一致。
func TestAssessMeasurementQualityDeterminism(t *testing.T) {
	for _, tc := range []struct {
		n   int
		sig bool
	}{{0, true}, {29, false}, {30, false}, {30, true}, {100, true}} {
		a := AssessMeasurementQuality(tc.n, tc.sig)
		b := AssessMeasurementQuality(tc.n, tc.sig)
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("n=%d sig=%t: not deterministic:\n%+v\n%+v", tc.n, tc.sig, a, b)
		}
	}
}

// TestAssessMeasurementQualityNoSharedState 外部修改一次调用的 Warnings，
// 不得影响后续调用或已有结果——证明没有共享可变切片（不改入参/无别名）。
func TestAssessMeasurementQualityNoSharedState(t *testing.T) {
	a := AssessMeasurementQuality(10, false)
	b := AssessMeasurementQuality(10, false)
	if len(a.Warnings) == 0 {
		t.Fatal("expected an insufficient-sample warning")
	}
	a.Warnings[0] = "（被外部修改）"
	if b.Warnings[0] != incompleteInsufficient {
		t.Errorf("returned Warnings share backing array: %q", b.Warnings[0])
	}
	c := AssessMeasurementQuality(10, false)
	if c.Warnings[0] != incompleteInsufficient {
		t.Errorf("mutation leaked into a fresh call: %q", c.Warnings[0])
	}
}
