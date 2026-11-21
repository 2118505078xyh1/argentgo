package eval

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// abResult 构造带名字与通过状态的 Result，便于测试配对。
func abResult(name string, passed bool) Result {
	return Result{Name: name, Passed: passed}
}

// TestRunVariantABPairsByFingerprint 证明配对按指纹而非顺序或名字：
// 两臂顺序不同、名字不同，只要指纹相同就对应到同一场景。
func TestRunVariantABPairsByFingerprint(t *testing.T) {
	// arm A 顺序：s1, s2, s3；arm B 顺序：s3', s1', s2'（乱序且改名）。
	baseline := []Result{
		abResult("s1", true), abResult("s2", false), abResult("s3", true),
	}
	candidate := []Result{
		abResult("s3'", true), abResult("s1'", false), abResult("s2'", false),
	}
	fpA := map[string]string{"s1": "fp-1", "s2": "fp-2", "s3": "fp-3"}
	fpB := map[string]string{"s1'": "fp-1", "s2'": "fp-2", "s3'": "fp-3"}

	got := RunVariantAB(baseline, candidate, fpA, fpB)
	if got.PairedCount != 3 {
		t.Fatalf("PairedCount = %d, want 3", got.PairedCount)
	}
	// Pairs 按指纹升序（fp-1 < fp-2 < fp-3），与输入顺序无关。
	want := []VariantABPair{
		{Fingerprint: "fp-1", BaselineName: "s1", CandidateName: "s1'", BaselinePassed: true, CandidatePassed: false},
		{Fingerprint: "fp-2", BaselineName: "s2", CandidateName: "s2'", BaselinePassed: false, CandidatePassed: false},
		{Fingerprint: "fp-3", BaselineName: "s3", CandidateName: "s3'", BaselinePassed: true, CandidatePassed: true},
	}
	if !reflect.DeepEqual(got.Pairs, want) {
		t.Fatalf("Pairs = %+v, want %+v", got.Pairs, want)
	}
	if len(got.Unmatched) != 0 {
		t.Fatalf("Unmatched = %+v, want empty", got.Unmatched)
	}
	for _, frag := range []string{"配对数 n=3", `A="s1"(通过) ↔ B="s1'"(失败)`} {
		if !strings.Contains(got.Text, frag) {
			t.Errorf("Text missing %q:\n%s", frag, got.Text)
		}
	}
}

// TestRunVariantABVerdictSignificant 用 30 对全翻转样本证明 verdict 走的是
// CompareResults 的显著性路径，而不是被配对逻辑改写。
func TestRunVariantABVerdictSignificant(t *testing.T) {
	base := make([]Result, 30)
	cand := make([]Result, 30)
	fpA := make(map[string]string, 30)
	fpB := make(map[string]string, 30)
	for i := 0; i < 30; i++ {
		idx := strconv.Itoa(i)
		nameA := "b" + idx
		nameB := "c" + idx
		base[i] = abResult(nameA, false)
		cand[i] = abResult(nameB, true)
		fp := "fp-" + idx
		fpA[nameA] = fp
		fpB[nameB] = fp
	}
	got := RunVariantAB(base, cand, fpA, fpB)
	if got.VerdictSummary != "显著提升" {
		t.Fatalf("VerdictSummary = %q, want 显著提升\n%s", got.VerdictSummary, got.Text)
	}
	if !strings.Contains(got.Text, "verdict: 显著提升：候选完成率更高") {
		t.Fatalf("Text missing significant verdict:\n%s", got.Text)
	}
	if got.PairedCount != 30 {
		t.Fatalf("PairedCount = %d, want 30", got.PairedCount)
	}
}

// TestRunVariantABInsufficientGuard 证明样本不足守卫在配对后依然生效：
// 6 个场景单翻转不得被误读为「候选更优」。
func TestRunVariantABInsufficientGuard(t *testing.T) {
	base := []Result{
		abResult("s0", false), abResult("s1", true), abResult("s2", true),
		abResult("s3", true), abResult("s4", true), abResult("s5", true),
	}
	cand := make([]Result, 6)
	fpA, fpB := map[string]string{}, map[string]string{}
	for i := 0; i < 6; i++ {
		cand[i] = abResult("s"+string(rune('0'+i)), true)
		fpA[base[i].Name] = "fp-" + string(rune('0'+i))
		fpB[cand[i].Name] = "fp-" + string(rune('0'+i))
	}
	got := RunVariantAB(base, cand, fpA, fpB)
	if got.VerdictSummary != "样本不足" {
		t.Fatalf("VerdictSummary = %q, want 样本不足\n%s", got.VerdictSummary, got.Text)
	}
	if !strings.Contains(got.Text, "样本不足") || !strings.Contains(got.Text, "不显著") {
		t.Fatalf("Text missing insufficient markers:\n%s", got.Text)
	}
	for _, forbidden := range []string{"verdict: 显著提升", "verdict: 显著正向"} {
		if strings.Contains(got.Text, forbidden) {
			t.Errorf("single flip must not claim improvement, found %q:\n%s", forbidden, got.Text)
		}
	}
}

// TestRunVariantABMissingFingerprint 证明非 nil 映射里查不到的场景被排除并标注原因，
// 不会与另一臂错配。
func TestRunVariantABMissingFingerprint(t *testing.T) {
	baseline := []Result{abResult("a", true), abResult("b", true)}
	candidate := []Result{abResult("a2", false), abResult("b2", false)}
	// arm A 缺 "b" 的指纹；arm B 缺 "b2" 的指纹。
	fpA := map[string]string{"a": "fp-a"}
	fpB := map[string]string{"a2": "fp-a"}

	got := RunVariantAB(baseline, candidate, fpA, fpB)
	if got.PairedCount != 1 || len(got.Pairs) != 1 {
		t.Fatalf("PairedCount = %d, Pairs = %+v, want 1 pair", got.PairedCount, got.Pairs)
	}
	wantUnmatched := []VariantABUnmatched{
		{Arm: variantArmBaseline, Name: "b", Reason: variantReasonMissingFP},
		{Arm: variantArmCandidate, Name: "b2", Reason: variantReasonMissingFP},
	}
	if !reflect.DeepEqual(got.Unmatched, wantUnmatched) {
		t.Fatalf("Unmatched = %+v, want %+v", got.Unmatched, wantUnmatched)
	}
	if !strings.Contains(got.Text, "指纹缺失") {
		t.Fatalf("Text missing 指纹缺失 marker:\n%s", got.Text)
	}
}

// TestRunVariantABNoCounterpart 证明指纹存在但另一臂没有的场景被标注「无对应指纹」。
func TestRunVariantABNoCounterpart(t *testing.T) {
	baseline := []Result{abResult("a", true), abResult("extra", true)}
	candidate := []Result{abResult("a2", false)}
	fpA := map[string]string{"a": "fp-a", "extra": "fp-extra"}
	fpB := map[string]string{"a2": "fp-a"}

	got := RunVariantAB(baseline, candidate, fpA, fpB)
	if got.PairedCount != 1 {
		t.Fatalf("PairedCount = %d, want 1", got.PairedCount)
	}
	want := []VariantABUnmatched{{
		Arm: variantArmBaseline, Name: "extra", Fingerprint: "fp-extra", Reason: variantReasonNoCounterpart,
	}}
	if !reflect.DeepEqual(got.Unmatched, want) {
		t.Fatalf("Unmatched = %+v, want %+v", got.Unmatched, want)
	}
}

// TestRunVariantABNilFingerprintsPairByName 证明两臂都不给指纹时退化为按名字配对。
func TestRunVariantABNilFingerprintsPairByName(t *testing.T) {
	baseline := []Result{abResult("同一场景", true)}
	candidate := []Result{abResult("同一场景", false)}
	got := RunVariantAB(baseline, candidate, nil, nil)
	if got.PairedCount != 1 || got.Pairs[0].Fingerprint != "同一场景" {
		t.Fatalf("unexpected pairing: %+v", got.Pairs)
	}
}

// TestRunVariantABCJK 证明中文场景名与 ScenarioFingerprint 生成的真实指纹都能稳定配对。
func TestRunVariantABCJK(t *testing.T) {
	scenarios := []Scenario{
		{Name: "读取笔记", Prompt: "notes.txt 里写了什么？"},
		{Name: "写入文件", Prompt: "把结果写到 out.txt"},
	}
	fps := FingerprintsFromScenarios(scenarios)
	if len(fps) != 2 {
		t.Fatalf(" FingerprintsFromScenarios len = %d, want 2", len(fps))
	}
	for _, s := range scenarios {
		if len(fps[s.Name]) != 64 {
			t.Fatalf("fingerprint for %q = %q, want 64 hex chars", s.Name, fps[s.Name])
		}
	}
	baseline := []Result{abResult("读取笔记", false), abResult("写入文件", true)}
	candidate := []Result{abResult("写入文件B", false), abResult("读取笔记B", true)}
	fpB := map[string]string{
		"读取笔记B": fps["读取笔记"],
		"写入文件B": fps["写入文件"],
	}

	got := RunVariantAB(baseline, candidate, fps, fpB)
	if got.PairedCount != 2 {
		t.Fatalf("PairedCount = %d, want 2\n%s", got.PairedCount, got.Text)
	}
	if got.Pairs[0].BaselineName != "写入文件" {
		// 指纹按十六进制升序排，"写入文件" 的指纹恰好更小或更大取决于哈希，这里不假定顺序。
		t.Logf("first pair baseline = %q", got.Pairs[0].BaselineName)
	}
	names := []string{got.Pairs[0].BaselineName, got.Pairs[1].BaselineName}
	if !reflect.DeepEqual(names, []string{"读取笔记", "写入文件"}) && !reflect.DeepEqual(names, []string{"写入文件", "读取笔记"}) {
		t.Fatalf("CJK names not paired correctly: %+v", got.Pairs)
	}
	if !strings.Contains(got.Text, "读取笔记") || !strings.Contains(got.Text, "写入文件") {
		t.Fatalf("Text missing CJK names:\n%s", got.Text)
	}
}

// TestRunVariantABDeterministic 证明输出与输入遍历顺序无关：同一组场景换序后
// Pairs、Unmatched 与 Text 完全一致。
func TestRunVariantABDeterministic(t *testing.T) {
	base := []Result{
		abResult("s1", true), abResult("s2", false), abResult("s3", true),
	}
	cand := []Result{
		abResult("s1b", false), abResult("s2b", true), abResult("s3b", true),
	}
	fpA := map[string]string{"s1": "fp-1", "s2": "fp-2", "s3": "fp-3"}
	fpB := map[string]string{"s1b": "fp-1", "s2b": "fp-2", "s3b": "fp-3"}

	a := RunVariantAB(base, cand, fpA, fpB)
	b := RunVariantAB(
		[]Result{base[2], base[0], base[1]},
		[]Result{cand[1], cand[2], cand[0]},
		fpA, fpB,
	)
	if !reflect.DeepEqual(a.Pairs, b.Pairs) {
		t.Fatalf("Pairs differ by input order:\n%+v\n%+v", a.Pairs, b.Pairs)
	}
	if a.Text != b.Text {
		t.Fatalf("Text differs by input order:\n%s\n---\n%s", a.Text, b.Text)
	}
}

// TestRunVariantABNoPairs 证明零配对时不调用 CompareResults 也能给出稳定结论与明细。
func TestRunVariantABNoPairs(t *testing.T) {
	got := RunVariantAB(
		[]Result{abResult("a", true)},
		[]Result{abResult("b", false)},
		map[string]string{"a": "fp-a"},
		map[string]string{"b": "fp-b"},
	)
	if got.VerdictSummary != "无有效配对" {
		t.Fatalf("VerdictSummary = %q, want 无有效配对", got.VerdictSummary)
	}
	for _, frag := range []string{"verdict: 无有效配对", "配对数 n=0", "配对: 无", "无对应指纹"} {
		if !strings.Contains(got.Text, frag) {
			t.Errorf("Text missing %q:\n%s", frag, got.Text)
		}
	}
}
