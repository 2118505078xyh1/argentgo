package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"example.com/argentgo/internal/eval"
)

// jsonFakeEvalRunner 是单场景路径的假 runner：它没有 expect 之外的主张，
// 因此只要场景未声明 expect_tool / expect_text，所有场景都判通过。
type jsonFakeEvalRunner struct {
	report EvalReport
}

func (f *jsonFakeEvalRunner) RunScenario(context.Context, string) (EvalReport, error) {
	return f.report, nil
}

// writeJSONEvalScenarios 写出 n 个只带 prompt 的场景，保证单场景路径全通过。
func writeJSONEvalScenarios(t *testing.T, n int) string {
	t.Helper()
	scenarios := make([]map[string]string, 0, n)
	for i := 0; i < n; i++ {
		scenarios = append(scenarios, map[string]string{"prompt": fmt.Sprintf("s%d", i)})
	}
	data, err := json.Marshal(scenarios)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "eval.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestEvalJSONExportsResultsAndFeedsCompare 是 R51 研究 #88 的主闭环：
// `eval --json` 导出的 Result 向量能被 loadResultVector 读回、含 11 条结果，
// 并且可以直接喂给 `eval compare out.json out.json`（退出 0）。
func TestEvalJSONExportsResultsAndFeedsCompare(t *testing.T) {
	evalPath := writeJSONEvalScenarios(t, 11)
	outPath := filepath.Join(filepath.Dir(evalPath), "out.json")

	stdio, out, errOut := newTestIO("", nil)
	app := App{IO: stdio, Eval: &jsonFakeEvalRunner{report: EvalReport{Text: "ok"}}}
	if code := RunWith(app, []string{"eval", "--json", outPath, evalPath}); code != 0 {
		t.Fatalf("exit = %d, stdout = %q, stderr = %q", code, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "11/11 scenarios passed") {
		t.Fatalf("stdout = %q", out.String())
	}

	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"results"`) {
		t.Fatalf("exported file must wrap results, got %s", raw)
	}
	results, err := loadResultVector(outPath)
	if err != nil {
		t.Fatalf("loadResultVector: %v", err)
	}
	if len(results) != 11 {
		t.Fatalf("len(results) = %d, want 11", len(results))
	}
	if first := results[0]; first.Index != 1 || first.Name != "s0" || !first.Passed {
		t.Fatalf("results[0] = %+v, want index=1 name=s0 passed=true", first)
	}
	if last := results[10]; last.Index != 11 || last.Name != "s10" {
		t.Fatalf("results[10] = %+v, want index=11 name=s10", last)
	}

	stdio2, _, errOut2 := newTestIO("", nil)
	if code := RunWith(App{IO: stdio2}, []string{"eval", "compare", outPath, outPath}); code != 0 {
		t.Fatalf("compare exit = %d, stderr = %q", code, errOut2.String())
	}
}

// TestEvalJSONOnlyWritesWhenPrefixPresent 守住"默认路径零变化"：不显式传
// --json 时一个字节都不落盘，只有传了前缀才写文件。
func TestEvalJSONOnlyWritesWhenPrefixPresent(t *testing.T) {
	evalPath := writeJSONEvalScenarios(t, 3)
	outPath := filepath.Join(filepath.Dir(evalPath), "out.json")

	stdio, out, errOut := newTestIO("", nil)
	app := App{IO: stdio, Eval: &jsonFakeEvalRunner{report: EvalReport{Text: "ok"}}}
	if code := RunWith(app, []string{"eval", evalPath}); code != 0 {
		t.Fatalf("default exit = %d, stderr = %q", code, errOut.String())
	}
	if _, err := os.Stat(outPath); !os.IsNotExist(err) {
		t.Fatalf("default path must not write %s, stat err = %v", outPath, err)
	}
	if !strings.Contains(out.String(), "3/3 scenarios passed") {
		t.Fatalf("default stdout = %q", out.String())
	}

	stdio2, _, errOut2 := newTestIO("", nil)
	app2 := App{IO: stdio2, Eval: &jsonFakeEvalRunner{report: EvalReport{Text: "ok"}}}
	if code := RunWith(app2, []string{"eval", "--json", outPath, evalPath}); code != 0 {
		t.Fatalf("--json exit = %d, stderr = %q", code, errOut2.String())
	}
	if _, err := os.Stat(outPath); err != nil {
		t.Fatalf("--json must write %s: %v", outPath, err)
	}
}

// jsonReportSuiteRunner 在 RunSuite 内真实跑一次 eval.Run，从而让包内的
// LastReport 拥有确定数量的 Result；它模拟生产 evaler 的 Result 来源。
type jsonReportSuiteRunner struct {
	n      int
	called bool
}

func (f *jsonReportSuiteRunner) RunSuite(ctx context.Context, _ string, _ io.Writer) (int, int, error) {
	f.called = true
	return f.run(ctx)
}

func (f *jsonReportSuiteRunner) run(ctx context.Context) (int, int, error) {
	scenarios := make([]eval.Scenario, f.n)
	for i := range scenarios {
		scenarios[i] = eval.Scenario{Name: fmt.Sprintf("s%d", i)}
	}
	runFunc := func(context.Context, eval.Scenario, eval.Workspace) (eval.Trace, error) {
		return eval.Trace{}, nil
	}
	report, err := eval.Run(ctx, scenarios, runFunc, eval.Options{Out: io.Discard})
	return report.Passed, report.Failed, err
}

// jsonReportLiveRunner 只实现 LiveEvalSuiteRunner，用来验证 --live 与 --json
// 共存时也能导出；两次调用顺序不同以覆盖"顺序任意"。
type jsonReportLiveRunner struct {
	n          int
	liveCalled bool
}

func (f *jsonReportLiveRunner) RunSuiteLive(ctx context.Context, _ string, _ io.Writer) (int, int, error) {
	f.liveCalled = true
	scenarios := make([]eval.Scenario, f.n)
	for i := range scenarios {
		scenarios[i] = eval.Scenario{Name: fmt.Sprintf("live-%d", i)}
	}
	runFunc := func(context.Context, eval.Scenario, eval.Workspace) (eval.Trace, error) {
		return eval.Trace{}, nil
	}
	report, err := eval.Run(ctx, scenarios, runFunc, eval.Options{Out: io.Discard})
	return report.Passed, report.Failed, err
}

// TestEvalJSONSuitePathExportsLastReport 覆盖生产用的套件路径：RunSuite 只回传
// 计数，CLI 从 eval.LastReport 取回 Results 后落盘。
func TestEvalJSONSuitePathExportsLastReport(t *testing.T) {
	evalPath := writeJSONEvalScenarios(t, 2)
	outPath := filepath.Join(filepath.Dir(evalPath), "suite-out.json")
	suite := &jsonReportSuiteRunner{n: 11}

	stdio, _, errOut := newTestIO("", nil)
	if code := RunWith(App{IO: stdio, Eval: suite}, []string{"eval", "--json", outPath, evalPath}); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	if !suite.called {
		t.Fatal("suite runner was not called")
	}
	results, err := loadResultVector(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 11 {
		t.Fatalf("len(results) = %d, want 11", len(results))
	}
}

// TestEvalJSONCombinesWithLive 验证 --live 与 --json 可同时出现、顺序任意，
// 且 live 套件跑出的 Result 向量同样落盘。
func TestEvalJSONCombinesWithLive(t *testing.T) {
	for _, tc := range []struct {
		name string
		args func(evalPath, outPath string) []string
	}{
		{name: "live first", args: func(e, o string) []string {
			return []string{"eval", "--live", "--json", o, e}
		}},
		{name: "json first", args: func(e, o string) []string {
			return []string{"eval", "--json", o, "--live", e}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			evalPath := writeJSONEvalScenarios(t, 1)
			outPath := filepath.Join(filepath.Dir(evalPath), "live-out.json")
			live := &jsonReportLiveRunner{n: 4}
			stdio, _, errOut := newTestIO("", nil)
			code := RunWith(App{IO: stdio, Eval: live}, tc.args(evalPath, outPath))
			if code != 0 {
				t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
			}
			if !live.liveCalled {
				t.Fatal("live runner was not called")
			}
			results, err := loadResultVector(outPath)
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != 4 {
				t.Fatalf("len(results) = %d, want 4", len(results))
			}
		})
	}
}

// TestEvalJSONWriteFailureReturnsNonZero 让落盘目标指向一个目录，验证导出失败
// 会被报告而不是静默吞掉。
func TestEvalJSONWriteFailureReturnsNonZero(t *testing.T) {
	evalPath := writeJSONEvalScenarios(t, 2)
	stdio, _, errOut := newTestIO("", nil)
	app := App{IO: stdio, Eval: &jsonFakeEvalRunner{report: EvalReport{Text: "ok"}}}
	if code := RunWith(app, []string{"eval", "--json", filepath.Dir(evalPath), evalPath}); code != 1 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "error:") {
		t.Fatalf("stderr = %q", errOut.String())
	}
}

// TestEvalJSONMissingPathArg 验证 --json 缺少路径参数时按用法错误退出。
func TestEvalJSONMissingPathArg(t *testing.T) {
	stdio, out, errOut := newTestIO("", nil)
	code := RunWith(App{IO: stdio, Eval: &jsonFakeEvalRunner{}}, []string{"eval", "--json"})
	if code != 2 {
		t.Fatalf("exit = %d, stdout = %q, stderr = %q", code, out.String(), errOut.String())
	}
	if !strings.Contains(errOut.String(), "usage: argentgo eval") {
		t.Fatalf("stderr = %q", errOut.String())
	}
}

// TestEvalUsageMentionsJSON 确保主 eval 用法提示包含 --json 前缀。
func TestEvalUsageMentionsJSON(t *testing.T) {
	var out strings.Builder
	evalUsage(&out)
	if !strings.Contains(out.String(), "--json") {
		t.Fatalf("eval usage does not mention --json:\n%s", out.String())
	}
}
