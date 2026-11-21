package eval

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// benchFixturePath 返回仓库 evals/completion_bench.json 的路径。
// 用 runtime.Caller 而非相对 cwd，使测试在任意工作目录下都能定位到 fixture。
func benchFixturePath(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test file")
	}
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "evals", "completion_bench.json")
}

// benchScriptTools 收集一个场景脚本中所有轮次请求的工具名。
func benchScriptTools(s Scenario) []string {
	var names []string
	for _, turn := range s.Provider {
		for _, call := range turn.Respond.ToolCalls {
			names = append(names, strings.TrimSpace(call.Name))
		}
	}
	return names
}

// benchNames 返回场景名序列，用于锁定 B1-B6 的顺序。
func benchNames(scenarios []Scenario) []string {
	names := make([]string, 0, len(scenarios))
	for _, s := range scenarios {
		names = append(names, s.Name)
	}
	return names
}

// TestCompletionBenchFixtureLoads 证明 R30 的 completion_bench.json 能被 eval.Load 解析，
// 且 B1-B6 的名字与顺序、脚本模式字段、客观 Check 都符合设计。
//
// 全部六个场景都是 Go 场景：allow_exec + go test ./...(want 0) + file_equals，
// 且每个场景的 file_equals 都逐字节锁死一份 *_test.go 种子内容，防止改测试作弊。
// B1/B3 额外声明 files_exist 覆盖 FilesExist 判据。
func TestCompletionBenchFixtureLoads(t *testing.T) {
	scenarios, err := Load(benchFixturePath(t))
	if err != nil {
		t.Fatalf("Load(completion_bench.json): %v", err)
	}
	if len(scenarios) != 6 {
		t.Fatalf("scenario count = %d, want 6", len(scenarios))
	}

	wantNames := []string{
		"B1 hashline anchored edit (should pass)",
		"B2 rename symbol across two files (should pass)",
		"B3 outline large file then precise edit (should pass)",
		"B4 ambiguous edit rejected then recovered (should pass)",
		"B5 negative control partial rename (should fail)",
		"B6 stale hashline anchor re-read then retry (should pass)",
	}
	if got := benchNames(scenarios); !reflect.DeepEqual(got, wantNames) {
		t.Fatalf("scenario names = %v, want %v", got, wantNames)
	}

	filesExistIndices := map[int]bool{0: true, 2: true}
	for i, s := range scenarios {
		t.Run(s.Name, func(t *testing.T) {
			// 脚本模式字段齐全：Provider、脚本工具、Expect.ToolCalls/TextContains/Turns 都必须非空。
			if len(s.Provider) == 0 {
				t.Error("provider must not be empty in script mode")
			}
			if tools := benchScriptTools(s); len(tools) == 0 {
				t.Error("script must request at least one tool call")
			}
			if len(s.Expect.ToolCalls) == 0 {
				t.Error("expect.tool_calls must be set in script mode")
			}
			if len(s.Expect.TextContains) == 0 {
				t.Error("expect.text_contains must be set in script mode")
			}
			if s.Expect.Turns == nil {
				t.Error("expect.turns must be set in script mode")
			}

			// 客观 Check：FileEquals 非空 + Go 场景判据 + 锁死 *_test.go 种子。
			if len(s.Check.FileEquals) == 0 {
				t.Fatal("check.file_equals must be present (objective grading)")
			}
			assertBenchGoCheck(t, s)

			if filesExistIndices[i] {
				if len(s.Check.FilesExist) == 0 {
					t.Error("this scenario must declare files_exist (FilesExist check)")
				}
			}
		})
	}
}

// assertBenchGoCheck 校验一个 Go 场景的客观判据：
// allow_exec + go test ./...(want 0) + file_equals，且 file_equals 锁死一份 *_test.go 种子内容。
func assertBenchGoCheck(t *testing.T, s Scenario) {
	t.Helper()
	if !s.Check.AllowExec || s.Check.Exec == nil {
		t.Fatal("check must enable exec")
	}
	exec := s.Check.Exec
	if exec.Command != "go" {
		t.Errorf("exec command = %q, want go", exec.Command)
	}
	if want := []string{"test", "./..."}; !reflect.DeepEqual(exec.Args, want) {
		t.Errorf("exec args = %v, want %v", exec.Args, want)
	}
	if exec.WantExitCode != 0 {
		t.Errorf("exec want_exit_code = %d, want 0", exec.WantExitCode)
	}

	locked := false
	for path, want := range s.Check.FileEquals {
		if !strings.HasSuffix(path, "_test.go") {
			continue
		}
		locked = true
		if got, ok := s.Files[path]; !ok || got != want {
			t.Errorf("file_equals locks %q to %q, but seed is %q (present=%t)", path, want, got, ok)
		}
	}
	if !locked {
		t.Error("check.file_equals must lock down a *_test.go seed to prevent test tampering")
	}
}

// TestCompletionBenchNamesDoNotCollide 证明 B1-B6 的名字与已有的 completion.json / completion_expanded.json 场景不重复。
func TestCompletionBenchNamesDoNotCollide(t *testing.T) {
	bench, err := Load(benchFixturePath(t))
	if err != nil {
		t.Fatalf("Load(completion_bench.json): %v", err)
	}
	seen := make(map[string]bool)
	for _, s := range bench {
		if seen[s.Name] {
			t.Errorf("duplicate scenario name %q", s.Name)
		}
		seen[s.Name] = true
	}

	// 已有场景集的名字必须全部不在新集合中。
	for _, path := range []string{completionFixturePath(t), expandedFixturePath(t)} {
		existing, err := Load(path)
		if err != nil {
			t.Fatalf("Load(%s): %v", path, err)
		}
		for _, s := range existing {
			if seen[s.Name] {
				t.Errorf("scenario name %q collides with an existing completion scenario", s.Name)
			}
		}
	}
}

// TestCompletionBenchNegativeSharesSeedAndCheck 锁定 B5 负对照与 B2 的契约：
// 同种子（files）、同 Check，只有脚本/期望不同，因此二者结果差异只能来自是否真的跨文件改对。
func TestCompletionBenchNegativeSharesSeedAndCheck(t *testing.T) {
	scenarios, err := Load(benchFixturePath(t))
	if err != nil {
		t.Fatalf("Load(completion_bench.json): %v", err)
	}
	b2, b5 := scenarios[1], scenarios[4]
	if !reflect.DeepEqual(b2.Files, b5.Files) {
		t.Error("B5 must share B2's seed files")
	}
	if !reflect.DeepEqual(b2.Check, b5.Check) {
		t.Error("B5 must share B2's Check byte-for-byte")
	}
	if reflect.DeepEqual(b2.Provider, b5.Provider) {
		t.Error("B5 script must differ from B2 (it renames only the definition)")
	}
}

// TestCompletionBenchScriptNeedles 锁定 B3/B4/B6 的关键脚本期望 needle：
// B3 必须回吐 outline 摘要（"outline:"），B4 必须回吐歧义错误文本
// （"appears 2 times"），B6 必须回吐过期锚点错误（"stale anchor"）。
//
// 这些 needle 是完成率 harness 判别力的来源。一旦它们从脚本期望里消失，
// 相应场景的 expect_request_contains 就不再校验 harness 是否真的产出了这些信息，
// 完成率也就失去对这些故障的敏感性。此测试是纯静态断言，零 API 成本。
func TestCompletionBenchScriptNeedles(t *testing.T) {
	scenarios, err := Load(benchFixturePath(t))
	if err != nil {
		t.Fatalf("Load(completion_bench.json): %v", err)
	}
	if len(scenarios) != 6 {
		t.Fatalf("scenario count = %d, want 6", len(scenarios))
	}

	// 索引与 B1-B6 一一对应：B3=2、B4=3、B6=5。
	want := map[int][]string{
		2: {"outline:"},
		3: {"appears 2 times"},
		5: {"stale anchor"},
	}
	for idx, needles := range want {
		var all []string
		for _, turn := range scenarios[idx].Provider {
			all = append(all, turn.ExpectRequestContains...)
		}
		for _, needle := range needles {
			found := false
			for _, got := range all {
				if strings.Contains(got, needle) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("%s: expect_request_contains must include %q, got %v",
					scenarios[idx].Name, needle, all)
			}
		}
	}
}

// compareResultsFromPass 把通过/失败序列构造为 Result 向量，供 compare 测试使用。
func compareResultsFromPass(passed []bool) []Result {
	out := make([]Result, len(passed))
	for i, p := range passed {
		out[i] = Result{Index: i + 1, Name: fmt.Sprintf("s%d", i+1), Passed: p}
	}
	return out
}

// TestCompareResultsVerdictSnapshot 用固定的 30 对向量锁定 verdict 文本格式：
// 前 10 个场景从失败翻转为通过（差值 +1），其余不变，配对均值差为正且 95%CI 不跨零，
// 应给出"显著提升"结论。期望值由 FormatDeltaReport 与固定 verdict 行拼成，
// 从而把 CompareResults 的"原语复用 + 结论选择"整体锁进快照。
// 这些 Result 未上报 usage，故末尾锁定的 token 诊断行是"无可比数据"保守说明。
func TestCompareResultsVerdictSnapshot(t *testing.T) {
	basePass := make([]bool, 30)
	candPass := make([]bool, 30)
	for i := range basePass {
		basePass[i] = i >= 10 // 前 10 个失败，后 20 个通过
		candPass[i] = true    // 候选全部通过
	}
	baseline := compareResultsFromPass(basePass)
	candidate := compareResultsFromPass(candPass)

	got, err := CompareResults(baseline, candidate)
	if err != nil {
		t.Fatalf("CompareResults: %v", err)
	}

	baseMetrics := make([]float64, len(basePass))
	candMetrics := make([]float64, len(candPass))
	for i := range basePass {
		baseMetrics[i] = passedMetric(Result{Passed: basePass[i]})
		candMetrics[i] = passedMetric(Result{Passed: candPass[i]})
	}
	delta, err := PairedDelta(baseMetrics, candMetrics)
	if err != nil {
		t.Fatalf("PairedDelta: %v", err)
	}
	want := FormatDeltaReport(compareReportName, Delta{N: delta.N}, delta) +
		"verdict: 显著提升：候选完成率更高\n" +
		FormatCompareDiagnostic(BuildCompareDiagnostic(baseline, candidate).Finalize(true)) +
		"token: 两臂 token 用量均为 0，无可比数据，跳过 token 经济学诊断\n"
	if got != want {
		t.Fatalf("CompareResults() mismatch\ngot:\n%s\nwant:\n%s", got, want)
	}
	for _, field := range []string{"配对诊断报告", "95%CI", "判定: 显著=true, 方向=正向", "verdict: 显著提升"} {
		if !strings.Contains(got, field) {
			t.Errorf("verdict missing %q:\n%s", field, got)
		}
	}
}

// TestCompareResultsSingleFlipNotSignificant 锁定背景约束：6 场景单翻转在统计上
// 几乎永不显著，必须输出"样本不足 / 不显著"，且绝不给出提升结论。
func TestCompareResultsSingleFlipNotSignificant(t *testing.T) {
	baseline := compareResultsFromPass([]bool{false, true, true, true, true, true})
	candidate := compareResultsFromPass([]bool{true, true, true, true, true, true}) // 仅翻转第 1 个

	// 先直接验证原始 Delta 确实不显著。
	d, err := PairedDelta([]float64{0, 1, 1, 1, 1, 1}, []float64{1, 1, 1, 1, 1, 1})
	if err != nil {
		t.Fatalf("PairedDelta: %v", err)
	}
	if ClassifyDelta(d, 0).Significant {
		t.Fatalf("single flip is unexpectedly significant: %+v", d)
	}

	got, err := CompareResults(baseline, candidate)
	if err != nil {
		t.Fatalf("CompareResults: %v", err)
	}
	for _, want := range []string{"样本不足", "n=6 < 30", "不显著"} {
		if !strings.Contains(got, want) {
			t.Errorf("verdict missing %q:\n%s", want, got)
		}
	}
	for _, forbidden := range []string{"显著提升", "显著正向", "候选完成率更高"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("single flip must not claim improvement, but verdict contains %q:\n%s", forbidden, got)
		}
	}
}

// TestCompareResultsInsufficientOverridesSignificant 证明样本不足的硬约束会覆盖
// 统计显著性：6 场景全部翻转时原始 CI 显著为正，但 verdict 仍必须是"样本不足"。
func TestCompareResultsInsufficientOverridesSignificant(t *testing.T) {
	baseline := compareResultsFromPass([]bool{false, false, false, false, false, false})
	candidate := compareResultsFromPass([]bool{true, true, true, true, true, true})

	got, err := CompareResults(baseline, candidate)
	if err != nil {
		t.Fatalf("CompareResults: %v", err)
	}
	verdictLine := ""
	for _, line := range strings.Split(strings.TrimSpace(got), "\n") {
		if strings.HasPrefix(line, "verdict:") {
			verdictLine = line
		}
	}
	if verdictLine == "" {
		t.Fatalf("no verdict line in:\n%s", got)
	}
	if !strings.Contains(verdictLine, "样本不足") || !strings.Contains(verdictLine, "不能判定候选更优") {
		t.Fatalf("verdict line must mark insufficient: %q", verdictLine)
	}
	if strings.Contains(verdictLine, "提升") || strings.Contains(verdictLine, "显著正向") {
		t.Fatalf("insufficient verdict must not claim improvement: %q", verdictLine)
	}
}

// TestCompareResultsErrorsAndPurity 覆盖非法向量、纯函数性与 n<2 的边界。
func TestCompareResultsErrorsAndPurity(t *testing.T) {
	if _, err := CompareResults(nil, nil); !errors.Is(err, ErrCompareVectors) {
		t.Fatalf("empty vectors error = %v, want ErrCompareVectors", err)
	}
	if _, err := CompareResults(compareResultsFromPass([]bool{true}),
		compareResultsFromPass([]bool{true, false})); !errors.Is(err, ErrCompareVectors) {
		t.Fatalf("unequal vectors error = %v, want ErrCompareVectors", err)
	}

	// n=1 无法估计方差，但应给出"样本不足"而非报错。
	one, err := CompareResults(compareResultsFromPass([]bool{false}),
		compareResultsFromPass([]bool{true}))
	if err != nil || !strings.Contains(one, "样本不足") {
		t.Fatalf("n=1 verdict = %q, err = %v", one, err)
	}

	base := compareResultsFromPass([]bool{true, false, true, false})
	cand := compareResultsFromPass([]bool{true, true, true, false})
	baseCopy := append([]Result(nil), base...)
	candCopy := append([]Result(nil), cand...)
	if _, err := CompareResults(base, cand); err != nil {
		t.Fatalf("CompareResults: %v", err)
	}
	if !reflect.DeepEqual(base, baseCopy) || !reflect.DeepEqual(cand, candCopy) {
		t.Fatal("CompareResults mutated its inputs")
	}
}
