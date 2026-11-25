package app

import (
	"bytes"
	"context"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"example.com/argentgo/internal/cli"
	"example.com/argentgo/internal/eval"
)

// expandedFixturePath 返回仓库 evals/completion_expanded.json 的路径。
// 用 runtime.Caller 而非相对 cwd，使测试在任意工作目录下都能定位到 fixture。
func expandedFixturePath(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test file")
	}
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "evals", "completion_expanded.json")
}

// TestEvalCompletionExpandedRate 是 R25 完成率粒度扩充的端到端证明。
//
// 它把 S1-S5 五个 JSON 场景交给 eval.Run，RunFunc 用 app 的真实 evaler.runScenario
// （真实 agent 循环 + 真实工具注册表 + 真实安全链，脚本化 provider，断网且无 API key）。
// 四个正例真修 bug → 通过；S5 负对照与 S2 同种子同 Check 但只修一个 bug → go test 退出 1 → 失败。
// 断言：4 通过 1 失败；ScenarioCompletionRate 复用长任务语义，恰好为 0.8；
// S5 的失败原因必须来自 exec 判据（"exited 1, want 0"）而非模型收尾自评。
func TestEvalCompletionExpandedRate(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}

	scenarios, err := eval.Load(expandedFixturePath(t))
	if err != nil {
		t.Fatalf("Load(completion_expanded.json): %v", err)
	}
	if len(scenarios) != 5 {
		t.Fatalf("scenario count = %d, want 5", len(scenarios))
	}

	runner := NewEvaler(cli.IO{
		Out: io.Discard,
		Err: io.Discard,
		Env: func(string) string { return "" },
	})
	var out bytes.Buffer
	report, runErr := eval.Run(
		context.Background(),
		scenarios,
		runner.(*evaler).runScenario,
		eval.Options{Out: &out},
	)
	// S5 应当失败，所以 eval.Run 返回非 nil 错误；报告仍然完整。
	if runErr == nil {
		t.Fatalf("eval.Run error = nil, want a failure for S5; report:\n%+v\n%s", report, out.String())
	}
	if report.Passed != 4 || report.Failed != 1 {
		t.Fatalf("report = %d passed, %d failed; want 4 and 1\n%+v\n%s",
			report.Passed, report.Failed, report, out.String())
	}
	// 完成率必须复用长任务语义：4/5 = 0.8。
	if got := eval.ScenarioCompletionRate(report.Results); math.Abs(got-0.8) > 1e-9 {
		t.Fatalf("ScenarioCompletionRate = %v, want 0.8; report:\n%+v\n%s", got, report, out.String())
	}

	// 前四个正例必须全过。
	for i := 0; i < 4; i++ {
		if !report.Results[i].Passed {
			t.Errorf("S%d (%s) should pass, reason: %s", i+1, report.Results[i].Name, report.Results[i].Reason)
		}
	}
	// 第五个负对照必须失败，且失败原因来自 go test 退出码而不是收尾文本。
	s5 := report.Results[4]
	if s5.Passed {
		t.Fatalf("S5 should fail, but passed; report:\n%+v\n%s", report, out.String())
	}
	if !strings.Contains(s5.Reason, "exited 1, want 0") {
		t.Fatalf("S5 reason = %q, want it to contain %q", s5.Reason, "exited 1, want 0")
	}
}

// TestEvalCompletionExpandedFileEqualsDiscriminatesWithoutExec 在纯 Check 层证明
// FileEquals 的独立判别力：即使完全关掉 Exec（不跑 go test），只做 FileEquals 的
// checkWorkspace 也能区分"部分修复"与"完整修复"。
//
// 做法是从 S2 取种子与 Check，关掉 allow_exec/exec 并清零脚本期望，用一个只把字节
// 写进工作区的 RunFunc 驱动 eval.Run（eval.Run 内部调用 checkWorkspace）。喂部分修复
// 字节（只修 Add）必须因 file_equals 不匹配而失败；喂完整修复字节必须通过。
func TestEvalCompletionExpandedFileEqualsDiscriminatesWithoutExec(t *testing.T) {
	scenarios, err := eval.Load(expandedFixturePath(t))
	if err != nil {
		t.Fatalf("Load(completion_expanded.json): %v", err)
	}
	s2 := scenarios[1]
	// 只留下 FileEquals 判据，隔离 Exec 与脚本期望。
	s2.Check.AllowExec = false
	s2.Check.Exec = nil
	s2.Expect = eval.Expect{}

	// 部分修复：Add 已改对，Negate 仍是种子里的 bug。
	const partialFixed = "package debug\n\nfunc Add(a, b int) int { return a + b }\n\nfunc Negate(x int) int { return x + 1 }\n"
	// 完整修复：两处都改对，逐字节等于 S2 Check 的期望。
	const fullFixed = "package debug\n\nfunc Add(a, b int) int { return a + b }\n\nfunc Negate(x int) int { return -x }\n"

	writeCalc := func(content string) eval.RunFunc {
		return func(_ context.Context, _ eval.Scenario, workspace eval.Workspace) (eval.Trace, error) {
			if err := os.WriteFile(filepath.Join(workspace.Dir, "calc.go"), []byte(content), 0o644); err != nil {
				return eval.Trace{}, err
			}
			return eval.Trace{}, nil
		}
	}

	t.Run("partial fix fails FileEquals", func(t *testing.T) {
		var out bytes.Buffer
		report, err := eval.Run(context.Background(), []eval.Scenario{s2}, writeCalc(partialFixed), eval.Options{Out: &out})
		if err == nil {
			t.Fatalf("eval.Run error = nil, want a FileEquals failure; report:\n%+v\n%s", report, out.String())
		}
		if report.Failed != 1 || report.Passed != 0 {
			t.Fatalf("report = %d passed, %d failed; want 0 and 1\n%+v\n%s",
				report.Passed, report.Failed, report, out.String())
		}
		if got := report.Results[0].Reason; !strings.Contains(got, "does not equal the expected bytes") {
			t.Fatalf("reason = %q, want a FileEquals byte-mismatch reason", got)
		}
	})

	t.Run("full fix passes FileEquals", func(t *testing.T) {
		var out bytes.Buffer
		report, err := eval.Run(context.Background(), []eval.Scenario{s2}, writeCalc(fullFixed), eval.Options{Out: &out})
		if err != nil {
			t.Fatalf("eval.Run error = %v, want nil; report:\n%+v\n%s", err, report, out.String())
		}
		if report.Passed != 1 || report.Failed != 0 {
			t.Fatalf("report = %d passed, %d failed; want 1 and 0\n%+v\n%s",
				report.Passed, report.Failed, report, out.String())
		}
	})
}

// TestEvalCompletionExpandedLiveSafeKeepsCheck 证明 S1-S5 交给 liveSafeScenarios 后：
// Expect 所有脚本字段与 Provider 全部归零，而 Check 逐字节保持原值。
// 这样真模型跑批只按工作区客观副作用评分，不受脚本化工具序列/轮数/措辞影响。
func TestEvalCompletionExpandedLiveSafeKeepsCheck(t *testing.T) {
	scenarios, err := eval.Load(expandedFixturePath(t))
	if err != nil {
		t.Fatalf("Load(completion_expanded.json): %v", err)
	}
	safe := liveSafeScenarios(scenarios)
	if len(safe) != len(scenarios) {
		t.Fatalf("liveSafeScenarios length = %d, want %d", len(safe), len(scenarios))
	}
	for i := range scenarios {
		original, got := scenarios[i], safe[i]
		if got.Provider != nil {
			t.Errorf("%s: live provider = %v, want nil", original.Name, got.Provider)
		}
		if got.Expect.ToolCalls != nil || got.Expect.Turns != nil ||
			got.Expect.Stopped != nil || got.Expect.TextContains != nil {
			t.Errorf("%s: live expect still has script fields: %+v", original.Name, got.Expect)
		}
		if !reflectDeepEqualCheck(original.Check, got.Check) {
			t.Errorf("%s: live check changed:\n got: %+v\nwant: %+v", original.Name, got.Check, original.Check)
		}
		// liveSafeScenarios 必须返回副本，不能改动入参。
		if scenarios[i].Provider == nil {
			t.Errorf("%s: liveSafeScenarios mutated the input scenarios", original.Name)
		}
	}
}

// reflectDeepEqualCheck 用 reflect.DeepEqual 比较两个 Check（含嵌套的 Exec 与 map）。
func reflectDeepEqualCheck(a, b eval.Check) bool {
	return reflect.DeepEqual(a, b)
}
