package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// evalScenario is one scripted eval case.
type evalScenario struct {
	Prompt     string `json:"prompt"`
	ExpectTool string `json:"expect_tool"`
	ExpectText string `json:"expect_text"`
}

// runEval implements `eval <file>`: it runs every scenario through the injected
// EvalRunner and grades tool usage and answer text.
func runEval(app App, ctx context.Context, args []string) int {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		evalUsage(app.IO.Out)
		return 0
	}
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		evalUsage(app.IO.Err)
		return 2
	}
	if app.Eval == nil {
		fmt.Fprintln(app.IO.Err, "error: eval runner not configured")
		return 1
	}

	data, err := os.ReadFile(args[0])
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	if suite, ok := app.Eval.(EvalSuiteRunner); ok {
		passed, failed, err := suite.RunSuite(ctx, args[0], app.IO.Out)
		if err != nil {
			fmt.Fprintf(app.IO.Err, "error: %s: %v\n", args[0], err)
			return 1
		}
		if failed > 0 {
			return 1
		}
		_ = passed
		return 0
	}
	scenarios, err := parseEvalFile(data)
	if err != nil {
		fmt.Fprintf(app.IO.Err, "error: %s: %v\n", args[0], err)
		return 1
	}
	if len(scenarios) == 0 {
		fmt.Fprintf(app.IO.Out, "no scenarios in %s\n", args[0])
		return 0
	}

	failures := 0
	for i, scenario := range scenarios {
		report, err := app.Eval.RunScenario(ctx, scenario.Prompt)
		if err != nil {
			fmt.Fprintf(app.IO.Out, "FAIL %d %q: runner error: %v\n", i+1, scenario.Prompt, err)
			failures++
			continue
		}
		if reason := gradeScenario(scenario, report); reason != "" {
			fmt.Fprintf(app.IO.Out, "FAIL %d %q: %s\n", i+1, scenario.Prompt, reason)
			failures++
			continue
		}
		fmt.Fprintf(app.IO.Out, "ok   %d %q\n", i+1, scenario.Prompt)
	}
	fmt.Fprintf(app.IO.Out, "%d/%d scenarios passed\n", len(scenarios)-failures, len(scenarios))
	if failures > 0 {
		return 1
	}
	return 0
}

func gradeScenario(scenario evalScenario, report EvalReport) string {
	if scenario.ExpectText != "" && !strings.Contains(report.Text, scenario.ExpectText) {
		return fmt.Sprintf("text does not contain %q (got %q)", scenario.ExpectText, clipText(report.Text, 80))
	}
	if scenario.ExpectTool != "" && !hasTool(report.Tools, scenario.ExpectTool) {
		return fmt.Sprintf("tool %q was not called (called: %s)", scenario.ExpectTool, joinOrNone(report.Tools))
	}
	return ""
}

func hasTool(tools []string, want string) bool {
	for _, tool := range tools {
		if tool == want {
			return true
		}
	}
	return false
}

func joinOrNone(tools []string) string {
	if len(tools) == 0 {
		return "none"
	}
	return strings.Join(tools, ", ")
}

func clipText(text string, limit int) string {
	fields := strings.Fields(text)
	joined := strings.Join(fields, " ")
	if len(joined) <= limit {
		return joined
	}
	return joined[:limit-1] + "…"
}

func parseEvalFile(data []byte) ([]evalScenario, error) {
	var scenarios []evalScenario
	if err := json.Unmarshal(data, &scenarios); err == nil {
		return scenarios, nil
	}
	var document struct {
		Scenarios []evalScenario `json:"scenarios"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, err
	}
	if document.Scenarios == nil {
		return nil, fmt.Errorf("expected a JSON array of scenarios or {\"scenarios\": [...]}")
	}
	return document.Scenarios, nil
}

func evalUsage(w io.Writer) {
	fmt.Fprint(w, `usage: argent eval <file.json>

The file holds [{"prompt": "...", "expect_tool": "...", "expect_text": "..."}],
or {"scenarios": [...]}. Exit code is non-zero when any scenario fails.
`)
}
