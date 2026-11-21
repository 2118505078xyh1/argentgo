package eval

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// compareNamed 构造带中文/自定义场景名的 Result，便于核对翻转明细里的名称与 0/1。
func compareNamed(pass bool, name string) Result {
	return Result{Name: name, Passed: pass}
}

// firstVerdictLine 取出报告中第一条以 "verdict:" 开头的行，用于锁定 verdict 文本不被诊断段改动。
func firstVerdictLine(report string) string {
	for _, line := range strings.Split(report, "\n") {
		if strings.HasPrefix(line, "verdict:") {
			return line
		}
	}
	return ""
}

// TestBuildCompareDiagnosticFlips 核对逐场景 0/1 翻转明细：方向、下标、名称与计数都要正确。
func TestBuildCompareDiagnosticFlips(t *testing.T) {
	baseline := []Result{
		compareNamed(true, "s0"),  // 通过→失败（loss）
		compareNamed(false, "s1"), // 不变
		compareNamed(true, "s2"),  // 不变
		compareNamed(false, "s3"), // 失败→通过（gain）
		compareNamed(false, ""),   // 不变的未命名字段
	}
	candidate := []Result{
		compareNamed(false, "s0"),
		compareNamed(false, "s1"),
		compareNamed(true, "s2"),
		compareNamed(true, "s3"),
		compareNamed(false, ""),
	}

	diag := BuildCompareDiagnostic(baseline, candidate)
	if diag.N != 5 {
		t.Errorf("N = %d, want 5", diag.N)
	}
	if diag.GuardThreshold != minComparePairs {
		t.Errorf("GuardThreshold = %d, want %d", diag.GuardThreshold, minComparePairs)
	}
	if !diag.Insufficient {
		t.Error("n=5 must trigger insufficient-sample guard")
	}
	if diag.FlipCount != 2 || diag.GainCount != 1 || diag.LossCount != 1 {
		t.Fatalf("flip counts = (%d, %d, %d), want (2, 1, 1)",
			diag.FlipCount, diag.GainCount, diag.LossCount)
	}
	wantFlips := []CompareFlip{
		{Index: 0, Name: "s0", Baseline: true, Candidate: false, Kind: flipLoss},
		{Index: 3, Name: "s3", Baseline: false, Candidate: true, Kind: flipGain},
	}
	if !reflect.DeepEqual(diag.Flips, wantFlips) {
		t.Fatalf("Flips = %+v, want %+v", diag.Flips, wantFlips)
	}

	text := FormatCompareDiagnostic(diag)
	for _, want := range []string{
		"翻转数: 2（失败→通过 1，通过→失败 1）",
		`场景 1 "s0": 通过→失败 (1→0)`,
		`场景 4 "s3": 失败→通过 (0→1)`,
		"样本不足守卫: 已触发",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("diagnostic missing %q:\n%s", want, text)
		}
	}
}

// TestCompareDiagnosticNoFlips 没有翻转时明细段应显式写"无"，而不是空白。
func TestCompareDiagnosticNoFlips(t *testing.T) {
	baseline := compareResultsFromPass([]bool{true, false, true})
	candidate := compareResultsFromPass([]bool{true, false, true})
	diag := BuildCompareDiagnostic(baseline, candidate)
	if diag.FlipCount != 0 || len(diag.Flips) != 0 {
		t.Fatalf("unexpected flips: %+v", diag.Flips)
	}
	if got := FormatCompareDiagnostic(diag); !strings.Contains(got, "翻转明细: 无") {
		t.Errorf("no-flip diagnostic missing marker:\n%s", got)
	}
}

// TestCompareDiagnosticSampleBoundary 覆盖 n 边界：守卫在 n<30 触发，n>=30 不触发；
// 同时锁定 CompareResults 在 n=29 与 n=30 的 verdict 分水岭。
func TestCompareDiagnosticSampleBoundary(t *testing.T) {
	for _, n := range []int{0, 1, 2, 6, 29, 30, 31} {
		passed := make([]bool, n)
		for i := range passed {
			passed[i] = true
		}
		base := compareResultsFromPass(passed)
		cand := compareResultsFromPass(passed)
		diag := BuildCompareDiagnostic(base, cand)
		if diag.N != n {
			t.Errorf("n=%d: diag.N = %d", n, diag.N)
		}
		if want := n < minComparePairs; diag.Insufficient != want {
			t.Errorf("n=%d: Insufficient = %t, want %t", n, diag.Insufficient, want)
		}
	}

	// n=29：样本不足守卫触发，verdict 必须是"样本不足"。
	base29 := compareResultsFromPass(repeatPass(29, true))
	got29, err := CompareResults(base29, compareResultsFromPass(repeatPass(29, true)))
	if err != nil {
		t.Fatalf("CompareResults(n=29): %v", err)
	}
	if !strings.Contains(firstVerdictLine(got29), "样本不足") {
		t.Errorf("n=29 verdict must be insufficient:\n%s", got29)
	}
	if !strings.Contains(got29, "样本不足守卫: 已触发") {
		t.Errorf("n=29 diagnostic must mark guard triggered:\n%s", got29)
	}

	// n=30：样本充足（守卫不触发），但全通过时差值不显著，verdict 走"差异不显著"。
	base30 := compareResultsFromPass(repeatPass(30, true))
	got30, err := CompareResults(base30, compareResultsFromPass(repeatPass(30, true)))
	if err != nil {
		t.Fatalf("CompareResults(n=30): %v", err)
	}
	if want := "verdict: 差异不显著：完成率差值区间的置信区间跨零，不能判定候选更优"; firstVerdictLine(got30) != want {
		t.Errorf("n=30 verdict = %q, want %q", firstVerdictLine(got30), want)
	}
	if !strings.Contains(got30, "样本不足守卫: 未触发") {
		t.Errorf("n=30 diagnostic must mark guard clear:\n%s", got30)
	}
	if !strings.Contains(got30, "不完整原因: 差异不显著") {
		t.Errorf("n=30 diagnostic must classify not-significant blind spot:\n%s", got30)
	}
}

// repeatPass 返回长度为 n、每项均为 pass 的切片。
func repeatPass(n int, pass bool) []bool {
	out := make([]bool, n)
	for i := range out {
		out[i] = pass
	}
	return out
}

// TestCompareResultsVerdictLinesUnchanged 锁定四类 verdict 行逐字不变，
// 证明追加诊断段没有改变任何 verdict 语义。
func TestCompareResultsVerdictLinesUnchanged(t *testing.T) {
	cases := []struct {
		name      string
		baseline  []bool
		candidate []bool
		want      string
	}{
		{
			name:      "样本不足",
			baseline:  []bool{false, true, true, true, true, true},
			candidate: []bool{true, true, true, true, true, true},
			want:      strings.TrimSpace(insufficientCompareVerdict(6)),
		},
		{
			name:      "不显著",
			baseline:  repeatPass(30, true),
			candidate: repeatPass(30, true),
			want:      "verdict: 差异不显著：完成率差值区间的置信区间跨零，不能判定候选更优",
		},
		{
			name:      "显著提升",
			baseline:  append(repeatPass(10, false), repeatPass(20, true)...),
			candidate: repeatPass(30, true),
			want:      "verdict: 显著提升：候选完成率更高",
		},
		{
			name:      "显著退化",
			baseline:  repeatPass(30, true),
			candidate: append(repeatPass(10, false), repeatPass(20, true)...),
			want:      "verdict: 显著退化：候选完成率更低",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CompareResults(compareResultsFromPass(tc.baseline), compareResultsFromPass(tc.candidate))
			if err != nil {
				t.Fatalf("CompareResults: %v", err)
			}
			if line := firstVerdictLine(got); line != tc.want {
				t.Errorf("verdict line = %q, want %q", line, tc.want)
			}
			if !strings.Contains(got, "诊断段:") {
				t.Errorf("diagnostic section missing:\n%s", got)
			}
			// 报告顺序：FormatDeltaReport 段 → verdict → 诊断段。
			deltaPart := strings.Index(got, "配对诊断报告:")
			verdictPart := strings.Index(got, "verdict:")
			diagPart := strings.Index(got, "诊断段:")
			if deltaPart < 0 || verdictPart < deltaPart || diagPart < verdictPart {
				t.Errorf("unexpected section order: delta=%d verdict=%d diag=%d\n%s",
					deltaPart, verdictPart, diagPart, got)
			}
		})
	}
}

// TestCompareResultsCJKAndEmptyVectors 覆盖中文场景名原样保留、未命名占位，以及空/不等长向量报错。
func TestCompareResultsCJKAndEmptyVectors(t *testing.T) {
	baseline := []Result{
		compareNamed(false, "场景一"),
		compareNamed(true, "场景二"),
		compareNamed(false, ""),
	}
	candidate := []Result{
		compareNamed(true, "场景一"),
		compareNamed(false, "场景二"),
		compareNamed(true, ""),
	}
	got, err := CompareResults(baseline, candidate)
	if err != nil {
		t.Fatalf("CompareResults: %v", err)
	}
	if !utf8.ValidString(got) {
		t.Error("report is not valid UTF-8")
	}
	for _, want := range []string{
		`场景 1 "场景一": 失败→通过 (0→1)`,
		`场景 2 "场景二": 通过→失败 (1→0)`,
		`场景 3 "（未命名）"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("CJK report missing %q:\n%s", want, got)
		}
	}

	if _, err := CompareResults(nil, nil); !errors.Is(err, ErrCompareVectors) {
		t.Errorf("nil vectors error = %v, want ErrCompareVectors", err)
	}
	if _, err := CompareResults([]Result{}, []Result{}); !errors.Is(err, ErrCompareVectors) {
		t.Errorf("empty vectors error = %v, want ErrCompareVectors", err)
	}
	if _, err := CompareResults(compareResultsFromPass([]bool{true}), nil); !errors.Is(err, ErrCompareVectors) {
		t.Errorf("unequal vectors error = %v, want ErrCompareVectors", err)
	}
	// 结构化构造器对空向量也必须安全，不 panic。
	if diag := BuildCompareDiagnostic(nil, nil); diag.N != 0 || len(diag.Flips) != 0 {
		t.Errorf("empty diagnostic = %+v", diag)
	}
}

// TestCompareResultsDiagnosticDeterminism 多次调用的输出与结构必须逐字节一致。
func TestCompareResultsDiagnosticDeterminism(t *testing.T) {
	basePass := make([]bool, 30)
	candPass := make([]bool, 30)
	for i := range basePass {
		basePass[i] = i%3 != 0 // 构造正负翻转混合，避免平凡全同
		candPass[i] = i%2 == 0
	}
	baseline := compareResultsFromPass(basePass)
	candidate := compareResultsFromPass(candPass)

	first, err := CompareResults(baseline, candidate)
	if err != nil {
		t.Fatalf("CompareResults: %v", err)
	}
	for i := 0; i < 5; i++ {
		again, err := CompareResults(baseline, candidate)
		if err != nil {
			t.Fatalf("CompareResults: %v", err)
		}
		if again != first {
			t.Fatalf("call %d differs from first:\n%s\n---\n%s", i, again, first)
		}
	}

	d1 := BuildCompareDiagnostic(baseline, candidate)
	d2 := BuildCompareDiagnostic(baseline, candidate)
	if !reflect.DeepEqual(d1, d2) {
		t.Fatalf("BuildCompareDiagnostic not deterministic:\n%+v\n%+v", d1, d2)
	}
	for i := 1; i < len(d1.Flips); i++ {
		if d1.Flips[i-1].Index >= d1.Flips[i].Index {
			t.Fatalf("flips not in ascending index order: %+v", d1.Flips)
		}
	}

	// Finalize 不改原值。
	finalized := d1.Finalize(true)
	if d1.IncompleteReason != "" {
		t.Errorf("Finalize mutated receiver: %q", d1.IncompleteReason)
	}
	if finalized.IncompleteReason == "" && d1.N < minComparePairs {
		t.Error("Finalize must classify insufficient samples")
	}
}
