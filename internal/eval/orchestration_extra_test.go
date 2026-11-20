package eval

import (
	"context"
	"strings"
	"testing"
)

// findOrchestrationScenario 返回磁盘上具有给定 ID 的场景。
func findOrchestrationScenario(t *testing.T, id string) OrchestrationScenario {
	t.Helper()
	for _, scenario := range loadOrchestration(t) {
		if scenario.ID == id {
			return scenario
		}
	}
	t.Fatalf("scenario %s is missing from orchestration.json", id)
	return OrchestrationScenario{}
}

// TestOrchestrationExtraScenariosLoadAndPass 固定扩展套件：文档至少包含十个场景，四个复杂场景存在，
// 文档至少包含十个场景，四个复杂场景存在，
// 且每个场景都通过真实循环/真实 task 工具。
func TestOrchestrationExtraScenariosLoadAndPass(t *testing.T) {
	scenarios := loadOrchestration(t)
	if len(scenarios) < 10 {
		t.Fatalf("orchestration.json holds %d scenarios, want at least 10", len(scenarios))
	}
	ids := make(map[string]bool, len(scenarios))
	for _, scenario := range scenarios {
		ids[scenario.ID] = true
		if len(scenario.Assertions) == 0 {
			t.Errorf("scenario %s has no assertions", scenario.ID)
		}
		if scenario.Kind != orchBoardKind && len(scenario.Turns) == 0 {
			t.Errorf("scenario %s has no scripted turns", scenario.ID)
		}
	}
	for _, want := range []string{"S6", "S7", "S8", "S9"} {
		if !ids[want] {
			t.Errorf("scenario %s is missing from orchestration.json", want)
		}
	}

	report := RunOrchestration(context.Background(), scenarios)
	t.Logf("expanded orchestration report: %d total, %d passed, %d failed", report.Total, report.Passed, report.Failed)
	if report.Total != len(scenarios) || report.Passed != len(scenarios) || report.Failed != 0 {
		for _, failure := range report.Failures {
			t.Errorf("%s (%s): %s", failure.ID, failure.Name, failure.Reason)
		}
		t.Fatalf("report = %d total, %d passed, %d failed; want %d/%d/0",
			report.Total, report.Passed, report.Failed, len(scenarios), len(scenarios))
	}
}

// TestOrchestrationExtraScenariosDriveTheRealLoop 记录新场景中什么是真实的：
// TestOrchestrationExtraScenariosDriveTheRealLoop 记录新场景中什么是真实的：不得走直接的 board-API 捷径，且每个必须
// 断言子运行次数加最终答案内容。
func TestOrchestrationExtraScenariosDriveTheRealLoop(t *testing.T) {
	for _, id := range []string{"S6", "S7", "S8", "S9"} {
		scenario := findOrchestrationScenario(t, id)
		if scenario.Kind == orchBoardKind {
			t.Errorf("scenario %s must drive the real loop, not the direct board API", id)
		}
		kinds := make(map[string]bool, len(scenario.Assertions))
		for _, a := range scenario.Assertions {
			kinds[a.Kind] = true
		}
		if !kinds["child_runs"] {
			t.Errorf("scenario %s does not assert child_runs", id)
		}
		if !kinds["final_text_contains"] {
			t.Errorf("scenario %s does not assert final_text_contains", id)
		}
	}
}

// TestOrchestrationExtraAssertionsDetectCorruption 证明新期望是真实的：每个场景破坏一个断言必须产生具体的
// 是真实的：每个场景破坏一个断言必须产生具体的
// 失败，原因中带断言名，绝不是静默通过。
func TestOrchestrationExtraAssertionsDetectCorruption(t *testing.T) {
	cases := []struct {
		id      string
		kind    string
		corrupt func(*OrchAssertion)
	}{
		{"S6", "child_runs", func(a *OrchAssertion) { v := 99; a.Equals = &v }},
		{"S7", "child_prompt_contains", func(a *OrchAssertion) { a.Value = "PROMPT_THAT_NEVER_RAN" }},
		{"S8", "final_text_contains", func(a *OrchAssertion) { a.Value = "TEXT_THAT_WAS_NEVER_SAID" }},
		{"S9", "verify_runs", func(a *OrchAssertion) { v := 99; a.Equals = &v }},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			scenario := findOrchestrationScenario(t, tc.id)
			found := false
			for i := range scenario.Assertions {
				if scenario.Assertions[i].Kind != tc.kind {
					continue
				}
				tc.corrupt(&scenario.Assertions[i])
				found = true
				break
			}
			if !found {
				t.Fatalf("scenario %s has no %s assertion to corrupt", tc.id, tc.kind)
			}

			report := RunOrchestration(context.Background(), []OrchestrationScenario{scenario})
			if report.Total != 1 || report.Passed != 0 || report.Failed != 1 {
				t.Fatalf("report = %+v, want 0 passed / 1 failed", report)
			}
			if len(report.Failures) != 1 || report.Failures[0].ID != tc.id {
				t.Fatalf("failures = %+v, want one failure for %s", report.Failures, tc.id)
			}
			if !strings.Contains(report.Failures[0].Reason, tc.kind) {
				t.Fatalf("failure reason = %q, want it to name %s", report.Failures[0].Reason, tc.kind)
			}
		})
	}
}

// 失败，原因中带有断言名，绝不是静默通过。
// TestOrchestrationMatchViolationsAreDetected 证明请求内容检查是承重的。S8 对真实 depth-guard 工具错误文本的匹配
// 是拒绝确实到达子级的断言，因此把它
// 改成循环无法产生的字符串必须使场景失败。
func TestOrchestrationMatchViolationsAreDetected(t *testing.T) {
	scenario := findOrchestrationScenario(t, "S8")
	corrupted := false
	for i := range scenario.Turns {
		for j := range scenario.Turns[i].Match {
			if scenario.Turns[i].Match[j] == "task depth limit reached" {
				scenario.Turns[i].Match[j] = "NO_SUCH_TOOL_ERROR_TEXT"
				corrupted = true
			}
		}
	}
	if !corrupted {
		t.Fatal("S8 does not match the real depth-limit tool-error text")
	}

	report := RunOrchestration(context.Background(), []OrchestrationScenario{scenario})
	if report.Total != 1 || report.Passed != 0 || report.Failed != 1 {
		t.Fatalf("report = %+v, want 0 passed / 1 failed", report)
	}
	if len(report.Failures) != 1 || report.Failures[0].ID != "S8" {
		t.Fatalf("failures = %+v, want one failure for S8", report.Failures)
	}
	if !strings.Contains(report.Failures[0].Reason, "request does not contain") {
		t.Fatalf("failure reason = %q, want a request-content violation", report.Failures[0].Reason)
	}
}
