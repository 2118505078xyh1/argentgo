package app

import (
	"math"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/eval"
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

// TestEvalCompletionBenchRate 是 R30 完成率 harness 基准的端到端证明。
//
// 它把 B1-B6 六个 JSON 场景交给 eval.Run，RunFunc 用 app 的真实 evaler.runScenario
// （真实 agent 循环 + 真实工具注册表 + 真实安全链，脚本化 provider，断网且无 API key）。
// B1/B2/B3/B4/B6 五个正例真修 bug → 通过；B5 负对照与 B2 同种子同 Check 但只改定义不改调用点
// → go test 编译失败退出 1 → 失败。断言：5 通过 1 失败；ScenarioCompletionRate = 5/6。
func TestEvalCompletionBenchRate(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}

	// 基线与敏感性自检共享同一份运行结果，省去一次 6 场景的真实循环。
	run := cachedCompletionBenchRun()
	if run.setupErr != nil {
		t.Fatalf("run completion bench: %v", run.setupErr)
	}
	report, runErr, tap := run.report, run.runErr, run.tap
	// B5 应当失败，所以 eval.Run 返回非 nil 错误；报告仍然完整。
	if runErr == nil {
		t.Fatalf("eval.Run error = nil, want a failure for B5; report:\n%+v\n%s", report, tap)
	}
	if report.Passed != 5 || report.Failed != 1 {
		t.Fatalf("report = %d passed, %d failed; want 5 and 1\n%+v\n%s",
			report.Passed, report.Failed, report, tap)
	}
	// 完成率必须复用长任务语义：5/6 ≈ 0.8333。
	wantRate := 5.0 / 6.0
	if got := eval.ScenarioCompletionRate(report.Results); math.Abs(got-wantRate) > 1e-9 {
		t.Fatalf("ScenarioCompletionRate = %v, want %v; report:\n%+v\n%s", got, wantRate, report, tap)
	}

	// 五个正例必须全过。
	positive := []int{0, 1, 2, 3, 5}
	for _, i := range positive {
		if !report.Results[i].Passed {
			t.Errorf("scenario %d (%s) should pass, reason: %s", i+1, report.Results[i].Name, report.Results[i].Reason)
		}
	}
	// B5（索引 4）负对照必须失败，且失败原因来自 go test 退出码而不是收尾文本。
	b5 := report.Results[4]
	if b5.Passed {
		t.Fatalf("B5 should fail, but passed; report:\n%+v\n%s", report, tap)
	}
	if !strings.Contains(b5.Reason, "exited 1, want 0") {
		t.Fatalf("B5 reason = %q, want it to contain %q", b5.Reason, "exited 1, want 0")
	}
}

// TestEvalCompletionBenchLiveSafeKeepsCheck 证明 B1-B6 交给 liveSafeScenarios 后：
// Expect 所有脚本字段与 Provider 全部归零，而 Check 逐字节保持原值。
// 这样真模型跑批只按工作区客观副作用评分，不受脚本化工具序列/轮数/措辞影响。
func TestEvalCompletionBenchLiveSafeKeepsCheck(t *testing.T) {
	scenarios, err := eval.Load(benchFixturePath(t))
	if err != nil {
		t.Fatalf("Load(completion_bench.json): %v", err)
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
		if !reflect.DeepEqual(original.Check, got.Check) {
			t.Errorf("%s: live check changed:\n got: %+v\nwant: %+v", original.Name, got.Check, original.Check)
		}
		// liveSafeScenarios 必须返回副本，不能改动入参。
		if scenarios[i].Provider == nil {
			t.Errorf("%s: liveSafeScenarios mutated the input scenarios", original.Name)
		}
	}
}
