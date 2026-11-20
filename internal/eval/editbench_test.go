package eval

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const editBenchFixture = "../../evals/edit_bench.json"

func loadBenchFixture(t *testing.T) []EditCase {
	t.Helper()
	cases, err := LoadEditCases(editBenchFixture)
	if err != nil {
		t.Fatalf("LoadEditCases(%q): %v", editBenchFixture, err)
	}
	return cases
}

// TestLoadEditCasesParsesFixture 检查 fixture 解码为格式良好的用例：唯一 ID、已知模式，以及至少一个期望。
// 格式良好的用例：唯一 ID、已知模式，以及至少一个期望。
func TestLoadEditCasesParsesFixture(t *testing.T) {
	cases := loadBenchFixture(t)
	if len(cases) < 20 {
		t.Fatalf("fixture has %d cases, want >= 20", len(cases))
	}
	seen := make(map[string]bool, len(cases))
	modes := make(map[string]bool)
	for i, c := range cases {
		if c.ID == "" {
			t.Errorf("case %d has an empty ID", i)
		}
		if seen[c.ID] {
			t.Errorf("case %d repeats ID %q", i, c.ID)
		}
		seen[c.ID] = true
		switch c.Mode {
		case modeExact, modeRepair, modeHashline:
		default:
			t.Errorf("case %s has invalid mode %q", c.ID, c.Mode)
		}
		modes[c.Mode] = true
		if c.WantResult == "" && c.WantErrContains == "" {
			t.Errorf("case %s declares no expectation", c.ID)
		}
		if strings.Contains(c.WantResult, "WantResult") || strings.Contains(c.WantErrContains, "WantErr") {
			t.Errorf("case %s contains a placeholder expectation", c.ID)
		}
	}
	for _, mode := range []string{modeExact, modeRepair, modeHashline} {
		if !modes[mode] {
			t.Errorf("fixture does not cover mode %q", mode)
		}
	}
}

// TestEditBenchFixtureCoversScenarios 守护基准的广度：它必须
// TestEditBenchFixtureCoversScenarios 守护基准的广度：它必须在每种模式下同时覆盖成功和拒绝的编辑，外加 UTF-8。
func TestEditBenchFixtureCoversScenarios(t *testing.T) {
	cases := loadBenchFixture(t)
	var success, failure, cjk, shifted int
	for _, c := range cases {
		if c.WantErrContains != "" {
			failure++
		} else {
			success++
		}
		if strings.ContainsAny(c.Original+c.Old+c.New+c.Anchor, "你好世界函数方法设置值中文锚点插入标题第一步第二步结束完成返回") {
			cjk++
		}
		if c.ShiftLines != "" {
			shifted++
		}
	}
	if success == 0 || failure == 0 {
		t.Fatalf("fixture must contain both success and failure cases (got %d success, %d failure)", success, failure)
	}
	if cjk < 4 {
		t.Errorf("fixture has only %d CJK/UTF-8 cases, want >= 4", cjk)
	}
	if shifted < 2 {
		t.Errorf("fixture has only %d shifted-anchor cases, want >= 2", shifted)
	}
}

// TestRunEditBenchMatchesFixtureExpectations 是核心断言：对每个用例，
// TestRunEditBenchMatchesFixtureExpectations 是核心断言：对每个用例，真实编辑工具（通过 tools.Registry.Run 调用）必须按
// fixture 所说的表现。一个总是通过的 harness 无法检测回归。
// regression.
func TestRunEditBenchMatchesFixtureExpectations(t *testing.T) {
	cases := loadBenchFixture(t)
	report := RunEditBench(cases)
	t.Logf("ACTUAL REAL-TOOL BENCHMARK NUMBERS: %s", report)
	if report.Total != len(cases) {
		t.Fatalf("report.Total = %d, want %d", report.Total, len(cases))
	}
	if len(report.Failures) != report.Failed {
		t.Fatalf("len(Failures) = %d, report.Failed = %d", len(report.Failures), report.Failed)
	}
	for _, failure := range report.Failures {
		t.Errorf("case %s (%s, %s) did not match expectation: got %s; reason: %s",
			failure.ID, failure.Name, failure.Mode, failure.Got, failure.Reason)
	}
	if report.Passed != len(cases) {
		t.Fatalf("pass rate %d/%d; %d cases disagreed with the real tool", report.Passed, len(cases), report.Failed)
	}
}

// TestRunEditBenchByModeAggregation 验证 ByMode 计数合计等于总数并与 fixture 的独立统计一致。
// 总数并与 fixture 的独立统计一致。
func TestRunEditBenchByModeAggregation(t *testing.T) {
	cases := loadBenchFixture(t)
	want := make(map[string]int)
	for _, c := range cases {
		want[c.Mode]++
	}
	report := RunEditBench(cases)
	if len(report.ByMode) != len(want) {
		t.Fatalf("ByMode has %d entries, want %d: %v", len(report.ByMode), len(want), report.ByMode)
	}
	sumPassed, sumFailed := 0, 0
	for mode, total := range want {
		stat, ok := report.ByMode[mode]
		if !ok {
			t.Fatalf("ByMode is missing mode %q", mode)
		}
		if got := stat.Passed + stat.Failed; got != total {
			t.Errorf("mode %s: Passed+Failed = %d, want %d", mode, got, total)
		}
		sumPassed += stat.Passed
		sumFailed += stat.Failed
	}
	if sumPassed != report.Passed || sumFailed != report.Failed {
		t.Errorf("ByMode sums (%d passed, %d failed) disagree with report (%d, %d)",
			sumPassed, sumFailed, report.Passed, report.Failed)
	}
	if report.Passed+report.Failed != report.Total {
		t.Errorf("Passed+Failed = %d, Total = %d", report.Passed+report.Failed, report.Total)
	}
}

// TestEditBenchReportString 检查摘要带有每种模式的通过率且跨调用稳定。
// 每种模式的通过率且跨调用稳定。
func TestEditBenchReportString(t *testing.T) {
	cases := loadBenchFixture(t)
	report := RunEditBench(cases)
	s := report.String()
	if !strings.Contains(s, "editbench:") || !strings.Contains(s, "passed") {
		t.Fatalf("summary missing totals: %q", s)
	}
	for _, mode := range []string{modeExact, modeRepair, modeHashline} {
		if !strings.Contains(s, mode) {
			t.Errorf("summary missing mode %q: %q", mode, s)
		}
	}
	if strings.Count(s, "100.0%") == 0 {
		t.Errorf("summary has no per-mode pass rate: %q", s)
	}
	if again := RunEditBench(cases).String(); again != s {
		t.Errorf("summary is not deterministic:\n%s\n%s", s, again)
	}
}

// TestEditArgsReflectDeclaredFieldsOnly 检查交给真实工具的 JSON 恰好携带用例声明的字段（加模式标志），使
// 工具恰好携带用例声明的字段（加模式标志），使
// 基准测量 fixture 的意图而非 harness 默认值。
func TestEditArgsReflectDeclaredFieldsOnly(t *testing.T) {
	cases := []struct {
		name   string
		tc     EditCase
		want   map[string]any
		absent []string
	}{
		{
			name:   "exact",
			tc:     EditCase{Mode: modeExact, Old: "a", New: "b"},
			want:   map[string]any{"path": editBenchFile, "old": "a", "new": "b"},
			absent: []string{"repair", "hashline", "anchor", "anchor_hash"},
		},
		{
			name:   "repair",
			tc:     EditCase{Mode: modeRepair, Old: "a", New: "b"},
			want:   map[string]any{"path": editBenchFile, "old": "a", "new": "b", "repair": true},
			absent: []string{"hashline", "anchor", "anchor_hash"},
		},
		{
			name:   "hashline",
			tc:     EditCase{Mode: modeHashline, Anchor: "a", AnchorHash: "deadbeefdead", New: "b"},
			want:   map[string]any{"path": editBenchFile, "new": "b", "anchor": "a", "anchor_hash": "deadbeefdead", "hashline": true},
			absent: []string{"repair", "old"},
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := editArgs(tt.tc)
			if err != nil {
				t.Fatalf("editArgs: %v", err)
			}
			var got map[string]any
			if err := json.Unmarshal([]byte(raw), &got); err != nil {
				t.Fatalf("decode args %s: %v", raw, err)
			}
			for key, want := range tt.want {
				value, ok := got[key]
				if !ok || value != want {
					t.Errorf("arg %q = %v (present=%t), want %v; raw=%s", key, value, ok, want, raw)
				}
			}
			for _, key := range tt.absent {
				if _, ok := got[key]; ok {
					t.Errorf("arg %q was sent but the case did not declare it; raw=%s", key, raw)
				}
			}
			if len(got) != len(tt.want) {
				t.Errorf("got %d args, want %d; raw=%s", len(got), len(tt.want), raw)
			}
		})
	}
}

// TestRunEditCaseUsesRealToolRegistry 证明 harness 到达已发布的 registry 而非本地解析器：只有 registry 强制
// registry 而非本地解析器：只有 registry 强制
// 修改类工具门，因此 yolo=false 必须在工具运行前拒绝编辑。
// yolo=true 时同一用例必须真正修改临时文件。
func TestRunEditCaseUsesRealToolRegistry(t *testing.T) {
	c := EditCase{
		ID: "registry-gate", Mode: modeExact,
		Original: "abc\n", Old: "abc", New: "xyz", WantResult: "xyz",
	}
	dir := t.TempDir()
	if _, err := seedEditFile(dir, c); err != nil {
		t.Fatalf("seedEditFile: %v", err)
	}
	_, err := runEditViaRegistry(dir, c, false)
	if err == nil || !strings.Contains(err.Error(), "rerun with --yolo") {
		t.Fatalf("registry mutation gate did not reject the edit: %v", err)
	}
	if _, err := runEditViaRegistry(dir, c, true); err != nil {
		t.Fatalf("yolo edit via real registry: %v", err)
	}
	edited, err := os.ReadFile(filepath.Join(dir, editBenchFile))
	if err != nil {
		t.Fatalf("read edited file: %v", err)
	}
	if !strings.Contains(string(edited), "xyz") {
		t.Fatalf("real tool did not mutate the file: %q", edited)
	}
}

// TestRealPathDetectsDeliberatelyWrongExpectation 证明基准不是
// TestRealPathDetectsDeliberatelyWrongExpectation 证明基准不是轻易通过：翻转 fixture 用例的期望后，harness 必须
// 报告带原因的失败。它检查两个方向（期望成功
// 报告带原因的失败。它检查两个方向（期望成功却应为错误，以及期望错误却应为成功）。
func TestRealPathDetectsDeliberatelyWrongExpectation(t *testing.T) {
	cases := loadBenchFixture(t)
	var successCase, errorCase EditCase
	for _, c := range cases {
		if c.WantErrContains == "" && successCase.ID == "" {
			successCase = c
		}
		if c.WantErrContains != "" && errorCase.ID == "" {
			errorCase = c
		}
	}
	if successCase.ID == "" || errorCase.ID == "" {
		t.Fatal("fixture must contain both success and error cases")
	}

	wrongResult := successCase
	wrongResult.WantResult = "THIS_TEXT_IS_NEVER_PRODUCED"
	if got := RunEditCase(wrongResult); got.Passed || got.Reason == "" {
		t.Fatalf("flipped success expectation was not detected: %+v", got)
	}

	wrongError := errorCase
	wrongError.WantErrContains = "THIS_ERROR_NEVER_APPEARS"
	if got := RunEditCase(wrongError); got.Passed || got.Reason == "" {
		t.Fatalf("flipped error expectation was not detected: %+v", got)
	}
}

// TestRunEditCaseDetectsMismatch 证明评分器非空：当观察到的结果与声明的期望矛盾时，它必须
// 报告失败，当观察到的结果与声明的期望矛盾时。
// 报告失败，两个方向都检查（期望成功与期望错误）。
func TestRunEditCaseDetectsMismatch(t *testing.T) {
	cases := []struct {
		name string
		tc   EditCase
		want bool
	}{
		{
			name: "expected success and got success",
			tc:   EditCase{ID: "ok-success", Mode: modeExact, Original: "abc\n", Old: "abc", New: "xyz", WantResult: "xyz"},
			want: true,
		},
		{
			name: "expected error and got matching error",
			tc:   EditCase{ID: "ok-error", Mode: modeExact, Original: "abc\n", Old: "missing", New: "x", WantErrContains: "not found"},
			want: true,
		},
		{
			name: "succeeded but expectation said error",
			tc:   EditCase{ID: "bad-success", Mode: modeExact, Original: "abc\n", Old: "abc", New: "x", WantErrContains: "not found"},
			want: false,
		},
		{
			name: "failed but expectation said success",
			tc:   EditCase{ID: "bad-error", Mode: modeExact, Original: "abc\n", Old: "missing", New: "x", WantResult: "abc"},
			want: false,
		},
		{
			name: "succeeded with wrong content",
			tc:   EditCase{ID: "bad-content", Mode: modeExact, Original: "abc\n", Old: "abc", New: "xyz", WantResult: "NOT_PRESENT"},
			want: false,
		},
		{
			name: "failed with a different error",
			tc:   EditCase{ID: "bad-error-text", Mode: modeExact, Original: "abc\n", Old: "missing", New: "x", WantErrContains: "not unique"},
			want: false,
		},
		{
			name: "unknown mode",
			tc:   EditCase{ID: "bad-mode", Mode: "bogus", Original: "abc\n", Old: "abc", New: "x", WantResult: "x"},
			want: false,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := RunEditCase(tt.tc)
			if got.Passed != tt.want {
				t.Fatalf("Passed = %t, want %t (got=%q reason=%q)", got.Passed, tt.want, got.Got, got.Reason)
			}
			if tt.want && got.Reason != "" {
				t.Fatalf("passing case has a reason: %q", got.Reason)
			}
			if !tt.want && got.Reason == "" {
				t.Fatal("failing case has no reason")
			}
		})
	}
}

// TestLoadEditCasesErrors 确认畸形文档被拒绝，而非静默评分为全通过。
// 而非静默评分为全通过。
func TestLoadEditCasesErrors(t *testing.T) {
	for _, doc := range []string{
		`{"cases":[]}`,
		`{"cases":[{"id":"a","mode":"nope","original":"x","new":"y","want_result":"y"}]}`,
		`{"cases":[{"id":"a","mode":"exact","original":"x","new":"y","want_result":"y"},{"id":"a","mode":"exact","original":"x","new":"y","want_result":"y"}]}`,
		`{"cases":[{"id":"a","mode":"exact","original":"x","new":"y"}]}`,
	} {
		if _, err := ParseEditCases([]byte(doc)); err == nil {
			t.Errorf("ParseEditCases accepted invalid document: %s", doc)
		}
	}
	// 裸数组也被接受。
	cases, err := ParseEditCases([]byte(`[{"id":"a","mode":"exact","original":"x","old":"x","new":"y","want_result":"y"}]`))
	if err != nil {
		t.Fatalf("bare array rejected: %v", err)
	}
	if len(cases) != 1 || cases[0].ID != "a" {
		t.Fatalf("bare array parsed as %+v", cases)
	}
}

// TestEditBenchFixturePath 确保文档记载的 fixture 位置能
// 从 `go test` 运行的包目录解析。
func TestEditBenchFixturePath(t *testing.T) {
	if _, err := filepath.Abs(editBenchFixture); err != nil {
		t.Fatal(err)
	}
	loadBenchFixture(t)
}

// ---- 相似度诊断（仅诊断，绝不作为依据）----

func TestLevenshteinKnownCases(t *testing.T) {
	cases := []struct {
		name, a, b string
		want       int
	}{
		{"both empty", "", "", 0},
		{"empty vs non-empty", "", "abc", 3},
		{"identical", "kitten", "kitten", 0},
		{"one substitution", "kitten", "kitteN", 1},
		{"classic kitten/sitting", "kitten", "sitting", 3},
		{"one insertion", "abc", "abcd", 1},
		{"one deletion", "abcd", "abc", 1},
		{"CJK identical", "你好世界", "你好世界", 0},
		{"CJK one edit", "你好世界", "你好世", 1},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := Levenshtein(tt.a, tt.b); got != tt.want {
				t.Fatalf("Levenshtein(%q,%q) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
	if Levenshtein("abc", "abcd") != Levenshtein("abcd", "abc") {
		t.Fatal("Levenshtein is not symmetric")
	}
	big := strings.Repeat("a", editSimMaxRunes+1)
	if got := Levenshtein(big, ""); got != -1 {
		t.Fatalf("Levenshtein(big,\"\") = %d, want -1 unbounded sentinel", got)
	}
	if got := Levenshtein("", big); got != -1 {
		t.Fatalf("Levenshtein(\"\",big) = %d, want -1 unbounded sentinel", got)
	}
}

func TestEditSimilarityBounds(t *testing.T) {
	cases := []struct {
		name, a, b string
		want       float64
	}{
		{"both empty", "", "", 1},
		{"identical", "abcd", "abcd", 1},
		{"one of four", "abcd", "abxd", 0.75},
		{"one of one", "a", "b", 0},
		{"CJK one of four", "你好世界", "你好世X", 0.75},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := EditSimilarity(tt.a, tt.b)
			if math.Abs(got-tt.want) > 1e-9 {
				t.Fatalf("EditSimilarity(%q,%q) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
			if got < 0 || got > 1 {
				t.Fatalf("EditSimilarity(%q,%q) = %v, want in [0,1]", tt.a, tt.b, got)
			}
		})
	}
	big := strings.Repeat("a", editSimMaxRunes+1)
	if got := EditSimilarity(big, "b"); got != -1 {
		t.Fatalf("unbounded EditSimilarity = %v, want -1 sentinel", got)
	}
}

func TestTokenExactMatch(t *testing.T) {
	cases := []struct {
		name, a, b string
		want       bool
	}{
		{"both empty", "", "", true},
		{"punctuation ignored", "hello, world!", "hello world", true},
		{"hyphen is a separator", "hello-world", "hello world", true},
		{"order matters", "a b c", "c b a", false},
		{"extra token", "a b", "a b c", false},
		{"digits kept", "v1.2.3", "v1 2 3", true},
		{"CJK letters kept", "你好世界", "你好 世界", false},
		{"CJK letters identical", "你好世界", "你好世界！", true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := TokenExactMatch(tt.a, tt.b); got != tt.want {
				t.Fatalf("TokenExactMatch(%q,%q) = %t, want %t", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestEditBenchSimilarityIsDiagnosticOnly(t *testing.T) {
	cases := []EditCase{
		{ID: "exact-hit", Mode: modeExact, Original: "abc\n", Old: "abc", New: "xyz", WantResult: "xyz\n"},
		// 产生的是 "abcdefghik\n" 而期望是 "abcdefghij\n"：只差一个 rune，所以
		// 相似度很高但权威的 Contains 检查必须失败。
		{ID: "close-but-wrong", Mode: modeExact, Original: "abcdefghij\n", Old: "abcdefghij", New: "abcdefghik", WantResult: "abcdefghij\n"},
	}
	report := RunEditBench(cases)
	if report.Total != 2 || report.Passed != 1 || report.Failed != 1 {
		t.Fatalf("authoritative counts changed: %+v", report)
	}
	if len(report.Failures) != 1 || report.Failures[0].ID != "close-but-wrong" {
		t.Fatalf("close-but-wrong case did not fail: %+v", report.Failures)
	}
	wrong := report.Failures[0]
	if wrong.Similarity <= 0 || wrong.Similarity >= 1 {
		t.Fatalf("close-but-wrong similarity = %v, want in (0,1)", wrong.Similarity)
	}
	if report.Scored != 2 {
		t.Fatalf("Scored = %d, want 2", report.Scored)
	}
	if report.ExactMatches != 1 {
		t.Fatalf("ExactMatches = %d, want 1", report.ExactMatches)
	}
	if report.MeanSimilarity <= 0 || report.MeanSimilarity >= 1 {
		t.Fatalf("MeanSimilarity = %v, want in (0,1)", report.MeanSimilarity)
	}
	stat := report.ByMode[modeExact]
	if stat.similarityN != 2 || stat.ExactMatches != 1 || math.Abs(stat.MeanSimilarity-report.MeanSimilarity) > 1e-9 {
		t.Fatalf("per-mode diagnostics disagree with report: %+v", stat)
	}

	// 交叉检查：记录的分数确实由产生的字节计算得出。
	dir := t.TempDir()
	c := cases[1]
	if _, err := seedEditFile(dir, c); err != nil {
		t.Fatal(err)
	}
	if _, err := runEditViaRegistry(dir, c, true); err != nil {
		t.Fatal(err)
	}
	final, err := os.ReadFile(filepath.Join(dir, editBenchFile))
	if err != nil {
		t.Fatal(err)
	}
	if want := EditSimilarity(string(final), c.WantResult); math.Abs(want-wrong.Similarity) > 1e-9 {
		t.Fatalf("recorded similarity %v != recomputed %v (final=%q)", wrong.Similarity, want, final)
	}

	s := report.String()
	for _, needle := range []string{"DIAGNOSTIC", "mean ES / EM", "ES=", "EM="} {
		if !strings.Contains(s, needle) {
			t.Errorf("summary %q missing %q", s, needle)
		}
	}
}

func TestEditBenchSimilaritySkipsUnbounded(t *testing.T) {
	big := strings.Repeat("x", editSimMaxRunes+1)
	c := EditCase{ID: "unbounded", Mode: modeExact, Original: "abc\n", Old: "abc", New: "xyz", WantResult: big}
	res := RunEditCase(c)
	if res.Similarity != -1 || res.scored {
		t.Fatalf("unbounded case must report -1 and be unscored: %+v", res)
	}
	report := RunEditBench([]EditCase{c})
	if report.Scored != 0 || report.MeanSimilarity != 0 || report.ExactMatches != 0 {
		t.Fatalf("unbounded case polluted diagnostics: %+v", report)
	}
}
