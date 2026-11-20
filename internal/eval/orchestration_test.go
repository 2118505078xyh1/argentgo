package eval

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func loadOrchestration(t *testing.T) []OrchestrationScenario {
	t.Helper()
	scenarios, err := LoadOrchestration(filepath.Join("..", "..", "evals", "orchestration.json"))
	if err != nil {
		t.Fatalf("LoadOrchestration(evals/orchestration.json): %v", err)
	}
	return scenarios
}

// 是拒绝确实到达子级的断言，因此把它改成循环无法产生的字符串必须使场景失败。
// TestOrchestrationSuitePasses 是端到端检查：所有场景都通过真实 agent 循环/真实 task 工具运行并必须通过其具体
// assertions.
func TestOrchestrationSuitePasses(t *testing.T) {
	scenarios := loadOrchestration(t)
	report := RunOrchestration(context.Background(), scenarios)
	t.Logf("orchestration report: %d total, %d passed, %d failed", report.Total, report.Passed, report.Failed)
	if report.Total != len(scenarios) {
		t.Fatalf("report.Total = %d, want %d", report.Total, len(scenarios))
	}
	if report.Failed != 0 || report.Passed != len(scenarios) {
		for _, failure := range report.Failures {
			t.Errorf("%s (%s): %s", failure.ID, failure.Name, failure.Reason)
		}
		t.Fatalf("report = %d passed, %d failed, want %d/0", report.Passed, report.Failed, len(scenarios))
	}
}

// TestOrchestrationSuiteCoverage 固定场景集合，使某个编排机制的静默删除无法悄悄降低覆盖率。
// 某个编排机制的静默删除无法悄悄降低覆盖率。
func TestOrchestrationSuiteCoverage(t *testing.T) {
	scenarios := loadOrchestration(t)
	if len(scenarios) < 10 {
		t.Fatalf("orchestration.json holds %d scenarios, want at least 10", len(scenarios))
	}
	ids := make(map[string]bool, len(scenarios))
	kinds := make(map[string]bool, len(scenarios))
	for _, scenario := range scenarios {
		ids[scenario.ID] = true
		kinds[scenario.Kind] = true
		if len(scenario.Assertions) == 0 {
			t.Errorf("scenario %s has no assertions", scenario.ID)
		}
	}
	for _, want := range []string{"S1", "S2", "S3a", "S3b", "S4", "S5", "S6", "S7", "S8", "S9"} {
		if !ids[want] {
			t.Errorf("scenario %s is missing from orchestration.json", want)
		}
	}
	if !kinds[orchBoardKind] {
		t.Error("the task-board direct API scenario is missing")
	}
}

// TestOrchestrationAssertionsDetectAWrongScenario 证明断言是
// TestOrchestrationAssertionsDetectAWrongScenario 证明断言是真实的：破坏一个期望计数必须使 harness 报告带
// 具体原因的失败，而非静默通过。
func TestOrchestrationAssertionsDetectAWrongScenario(t *testing.T) {
	scenarios := loadOrchestration(t)
	var wrong OrchestrationScenario
	for _, scenario := range scenarios {
		if scenario.ID == "S1" {
			wrong = scenario
			break
		}
	}
	if wrong.ID != "S1" {
		t.Fatal("scenario S1 is missing")
	}
	bad := 99
	for i := range wrong.Assertions {
		if wrong.Assertions[i].Kind == "child_runs" {
			wrong.Assertions[i].Equals = &bad
		}
	}

	report := RunOrchestration(context.Background(), []OrchestrationScenario{wrong})
	if report.Total != 1 || report.Passed != 0 || report.Failed != 1 {
		t.Fatalf("report = %+v, want 0 passed / 1 failed", report)
	}
	if len(report.Failures) != 1 || report.Failures[0].ID != "S1" {
		t.Fatalf("failures = %+v, want one failure for S1", report.Failures)
	}
	if !strings.Contains(report.Failures[0].Reason, "child_runs") {
		t.Fatalf("failure reason = %q, want it to name the child-run count", report.Failures[0].Reason)
	}
}

// TestOrchestrationReportCountsKeepsGoing 检查一个坏场景不会停止套件且报告保持完整。
// 停止套件且报告保持完整。
func TestOrchestrationReportCountsKeepsGoing(t *testing.T) {
	scenarios := loadOrchestration(t)
	bad := scenarios[0]
	bad.ID = "BAD"
	for i := range bad.Assertions {
		if bad.Assertions[i].Kind == "child_runs" {
			value := -1
			bad.Assertions[i].Equals = &value
		}
	}
	mixed := append([]OrchestrationScenario{bad}, scenarios[1:]...)
	report := RunOrchestration(context.Background(), mixed)
	if report.Total != len(mixed) {
		t.Fatalf("Total = %d, want %d", report.Total, len(mixed))
	}
	if report.Failed != 1 || report.Passed != len(mixed)-1 {
		t.Fatalf("report = %d passed, %d failed; want %d/1", report.Passed, report.Failed, len(mixed)-1)
	}
}

// TestParseOrchestrationAcceptsABareArray 覆盖加载器宣称的第二种文档形态。
// 加载器宣称的第二种文档形态。
func TestParseOrchestrationAcceptsABareArray(t *testing.T) {
	data := []byte(`[{"id":"X","prompt":"p","turns":[],"assertions":[]}]`)
	scenarios, err := ParseOrchestration(data)
	if err != nil {
		t.Fatalf("ParseOrchestration: %v", err)
	}
	if len(scenarios) != 1 || scenarios[0].ID != "X" || scenarios[0].Name == "" {
		t.Fatalf("scenarios = %+v", scenarios)
	}
}
