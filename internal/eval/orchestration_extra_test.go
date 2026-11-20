package eval

import (
	"context"
	"strings"
	"testing"
)

// findOrchestrationScenario returns the on-disk scenario with the given ID.
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

// TestOrchestrationExtraScenariosLoadAndPass pins the expanded suite: the
// document holds at least ten scenarios, the four complex ones are present,
// and every scenario passes through the real loop / real task tool.
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

// TestOrchestrationExtraScenariosDriveTheRealLoop documents what is REAL in the
// new scenarios: none may take the direct board-API shortcut, and each must
// assert child-run counts plus final answer content.
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

// TestOrchestrationExtraAssertionsDetectCorruption proves the new expectations
// are real: corrupting one assertion per scenario must produce a concrete
// failure with the assertion's name in the reason, never a silent pass.
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

// TestOrchestrationMatchViolationsAreDetected proves the request-content
// checks are load-bearing. S8's match on the real depth-guard tool-error text
// is the assertion that the refusal really reached the child, so changing it
// to a string the loop cannot produce must fail the scenario.
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
