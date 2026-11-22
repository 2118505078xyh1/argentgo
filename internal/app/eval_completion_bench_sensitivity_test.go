package app

import (
	"bytes"
	"context"
	"io"
	"math"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/eval"
)

// 本文件是 R30 completion_bench 的敏感性自检：零生产改动、零 API 成本。
//
// 思路：把场景脚本里承载 harness 判别力的关键 expect_request_contains needle
// 改坏（模拟"工具不再回吐可读错误/摘要"这类 harness 故障），再用真实 agent 循环 +
// 真实工具注册表 + 真实安全链（脚本化 provider、断网、无 API key）重跑。
// 若完成率对这些故障无动于衷，说明 bench 已失去判别力，本测试即失败。
//
// 注入的故障分别命中：
//   - B1 hashline 锚点编辑（破坏锚点行回注）
//   - B3 大文件大纲定位（破坏 "outline:" 摘要回注）
//   - B4 歧义 edit 错误（破坏 "appears 2 times" 文本）
//   - B6 过期锚点错误（破坏 "stale anchor" 文本）
//
// B2（普通跨文件重命名）不依赖上述任何 needle，作为对照必须始终通过。

// benchSensitivityReport 用真实 evaler.runScenario 跑一遍场景，返回完整报告。
// B5 负对照恒失败，因此 eval.Run 返回的非 nil 错误在此忽略，仅用于诊断。
func benchSensitivityReport(t *testing.T, scenarios []eval.Scenario) eval.Report {
	t.Helper()
	runner := NewEvaler(cli.IO{
		Out: io.Discard,
		Err: io.Discard,
		Env: func(string) string { return "" },
	})
	var out bytes.Buffer
	report, _ := eval.Run(
		context.Background(),
		scenarios,
		runner.(*evaler).runScenario,
		eval.Options{Out: &out},
	)
	if len(report.Results) != len(scenarios) {
		t.Fatalf("report has %d results, want %d; TAP:\n%s",
			len(report.Results), len(scenarios), out.String())
	}
	return report
}

// benchSensitivityReportFor 只跑 indices 指定的场景，并把结果的 Index 修正回
// 原场景下标。故障注入只影响目标场景，其余场景在基线里已经验证过，不必在每个
// 故障用例里重跑全部 6 个真实循环（那会让单个子测试耗时 ~7.6s）。
func benchSensitivityReportFor(t *testing.T, scenarios []eval.Scenario, indices ...int) eval.Report {
	t.Helper()
	subset := make([]eval.Scenario, 0, len(indices))
	for _, i := range indices {
		subset = append(subset, scenarios[i])
	}
	report := benchSensitivityReport(t, subset)
	for k, idx := range indices {
		if k < len(report.Results) {
			report.Results[k].Index = idx
		}
	}
	return report
}

// breakBenchNeedle 把指定场景某个 expect_request_contains needle 改坏，
// 模拟 harness 不再产出该文本。返回是否找到并替换。
func breakBenchNeedle(scenarios []eval.Scenario, idx int, needle string) bool {
	found := false
	for ti := range scenarios[idx].Provider {
		for ni, got := range scenarios[idx].Provider[ti].ExpectRequestContains {
			if got == needle {
				scenarios[idx].Provider[ti].ExpectRequestContains[ni] = "SENSITIVITY_FAULT_" + needle
				found = true
			}
		}
	}
	return found
}

// TestCompletionBenchSensitiveToHarnessFaults 是敏感性自检主体：
// 基线 5/6，注入任一故障后目标场景失败、完成率降到 4/6，而对照 B2 不受影响。
func TestCompletionBenchSensitiveToHarnessFaults(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}

	// 1. 先测基线：五个正例通过、B5 负对照失败，完成率 5/6。
	baseScenarios, err := eval.Load(benchFixturePath(t))
	if err != nil {
		t.Fatalf("Load(completion_bench.json): %v", err)
	}
	baseReport := benchSensitivityReport(t, baseScenarios)
	baseRate := eval.ScenarioCompletionRate(baseReport.Results)
	if math.Abs(baseRate-5.0/6.0) > 1e-9 {
		t.Fatalf("baseline completion rate = %v, want 5/6", baseRate)
	}
	if !baseReport.Results[1].Passed {
		t.Fatalf("baseline B2 (control) must pass, reason: %s", baseReport.Results[1].Reason)
	}
	for _, i := range []int{0, 2, 3, 5} {
		if !baseReport.Results[i].Passed {
			t.Fatalf("baseline positive scenario %d must pass, reason: %s", i+1, baseReport.Results[i].Reason)
		}
	}

	// 2. 逐项注入故障，断言完成率下降且对照 B2 不变。
	faults := []struct {
		name   string
		idx    int
		needle string
	}{
		{name: "B1 hashline anchor", idx: 0, needle: "return a - b"},
		{name: "B3 outline", idx: 2, needle: "outline: big.go"},
		{name: "B4 ambiguous edit error", idx: 3, needle: "appears 2 times"},
		{name: "B6 stale anchor error", idx: 5, needle: "stale anchor"},
	}
	for _, f := range faults {
		t.Run(f.name, func(t *testing.T) {
			// 各故障用例只改自己的场景副本，彼此无共享可变状态；
			// 每轮都要跑 6 个真实循环，并行可把这段从 ~30s 压到 ~8s。
			t.Parallel()
			scenarios, err := eval.Load(benchFixturePath(t))
			if err != nil {
				t.Fatalf("Load(completion_bench.json): %v", err)
			}
			if !breakBenchNeedle(scenarios, f.idx, f.needle) {
				t.Fatalf("needle %q not found in scenario %d (%s)", f.needle, f.idx, scenarios[f.idx].Name)
			}

			// 只重跑目标场景和对照 B2；其余场景在基线中已验证，直接复用其
			// 结果合成完整报告，下面的完成率与逐项断言保持原样不变。
			partial := benchSensitivityReportFor(t, scenarios, f.idx, 1)
			report := eval.Report{Results: append([]eval.Result(nil), baseReport.Results...)}
			report.Results[f.idx] = partial.Results[0]
			report.Results[1] = partial.Results[1]
			rate := eval.ScenarioCompletionRate(report.Results)
			if rate >= baseRate {
				t.Fatalf("completion rate did not drop after fault: got %v, baseline %v; reasons: %s",
					rate, baseRate, strings.Join([]string{
						report.Results[0].Reason, report.Results[2].Reason,
						report.Results[3].Reason, report.Results[5].Reason,
					}, " | "))
			}
			if math.Abs(rate-4.0/6.0) > 1e-9 {
				t.Fatalf("completion rate = %v, want 4/6 after one fault", rate)
			}
			if report.Results[f.idx].Passed {
				t.Fatalf("faulted scenario %d (%s) still passed; bench is not sensitive to this fault",
					f.idx+1, scenarios[f.idx].Name)
			}
			// B2 是对照：所有故障都不应波及它。
			if !report.Results[1].Passed {
				t.Fatalf("control B2 must stay passing under fault %q, reason: %s",
					f.name, report.Results[1].Reason)
			}
			// 其余正例与 B5 负对照保持原判。
			for _, i := range []int{0, 2, 3, 5} {
				if i == f.idx {
					continue
				}
				if !report.Results[i].Passed {
					t.Errorf("non-target positive scenario %d should still pass, reason: %s",
						i+1, report.Results[i].Reason)
				}
			}
			if report.Results[4].Passed {
				t.Error("B5 negative control should still fail")
			}
		})
	}
}

// TestApprovalDenialScenarioSensitiveToRefusalNeedle 证明审批否决场景的判别力：
// 把"refused by the operator"这根回注 needle 改坏（模拟安全链不再回吐可读拒绝文本），
// 脚本化 provider 的 expect_request_contains 就会失败，场景不再通过。
// 与 bench 的敏感性自检同理：完成率必须能感知 harness 故障，而非恒为 1。
func TestApprovalDenialScenarioSensitiveToRefusalNeedle(t *testing.T) {
	const name = "approval denial returns a readable final error"
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test file")
	}
	path := filepath.Join(filepath.Dir(thisFile), "..", "..", "evals", "core.json")
	scenarios, err := eval.Load(path)
	if err != nil {
		t.Fatalf("Load(core.json): %v", err)
	}
	idx := -1
	for i := range scenarios {
		if scenarios[i].Name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatalf("core.json has no scenario %q", name)
	}
	if !breakBenchNeedle(scenarios, idx, "refused by the operator") {
		t.Fatalf("needle %q not found in scenario %q", "refused by the operator", name)
	}

	report := benchSensitivityReport(t, []eval.Scenario{scenarios[idx]})
	if report.Results[0].Passed {
		t.Fatal("scenario still passed after breaking the refusal needle; it has no discriminative power")
	}
	reason := report.Results[0].Reason
	if !strings.Contains(reason, "SENSITIVITY_FAULT_refused by the operator") {
		t.Fatalf("failure reason = %q, want the broken needle to be reported", reason)
	}
}
