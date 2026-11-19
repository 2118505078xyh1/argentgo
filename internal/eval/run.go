package eval

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/2118505078xyh1/argent-go/internal/llm"
)

// Workspace is everything a RunFunc needs for one scenario.
type Workspace struct {
	// Dir is the seeded scratch directory, which must be the loop's cwd.
	Dir    string
	Prompt string
	System string
	Model  string
	// Chatter is the scripted provider for this scenario. A runner that talks
	// to a provider directly (instead of a loop that takes one) uses this.
	Chatter llm.Chatter
}

// Trace is what a run reports about itself.
type Trace struct {
	// Tools are the tool names that ran, in order.
	Tools []string
	// Text is the final assistant answer.
	Text string
	// Stopped reports that something other than the model ended the run: a
	// turn limit, a policy block, a cancellation.
	Stopped bool
}

// RunFunc executes one scenario. The harness seeds the workspace and grades the
// result; the caller owns the loop, so this package never imports the agent.
//
// Returning context.Canceled or context.DeadlineExceeded is treated as a normal
// cancellation, not a failure.
type RunFunc func(ctx context.Context, scenario Scenario, workspace Workspace) (Trace, error)

// Options configures a suite run.
type Options struct {
	// Verbose adds a TAP diagnostic line per scenario.
	Verbose bool
	// Out receives the TAP-ish report. Defaults to os.Stdout.
	Out io.Writer
	// Keep leaves the scratch directories behind, for debugging a failure.
	Keep bool
}

// Result is the graded outcome of one scenario.
type Result struct {
	Index   int
	Name    string
	Passed  bool
	Reason  string
	Tools   []string
	Text    string
	Turns   int
	Stopped bool
}

// Report summarises a suite run.
type Report struct {
	Passed  int
	Failed  int
	Results []Result
}

// Run executes every scenario and prints a TAP-ish report.
//
// It returns a non-nil error when any scenario fails; the report is still
// complete, so a caller can print both.
func Run(ctx context.Context, scenarios []Scenario, run RunFunc, opts Options) (Report, error) {
	if run == nil {
		return Report{}, errors.New("eval: RunFunc is nil")
	}
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}

	report := Report{Results: make([]Result, 0, len(scenarios))}
	fmt.Fprintf(out, "1..%d\n", len(scenarios))
	for i, scenario := range scenarios {
		result := runOne(ctx, i+1, scenario, run, opts)
		report.Results = append(report.Results, result)
		if result.Passed {
			report.Passed++
			fmt.Fprintf(out, "ok %d - %s\n", result.Index, result.Name)
		} else {
			report.Failed++
			fmt.Fprintf(out, "not ok %d - %s: %s\n", result.Index, result.Name, oneLine(result.Reason))
		}
		if opts.Verbose {
			fmt.Fprintf(out, "#   tools=%v turns=%d stopped=%t text=%q\n",
				result.Tools, result.Turns, result.Stopped, clip(result.Text, 120))
		}
	}
	fmt.Fprintf(out, "# %d passed, %d failed\n", report.Passed, report.Failed)
	if report.Failed > 0 {
		return report, fmt.Errorf("eval: %d of %d scenarios failed", report.Failed, len(scenarios))
	}
	return report, nil
}

// runOne seeds a scratch workspace, runs the scenario there and grades it.
func runOne(ctx context.Context, index int, scenario Scenario, run RunFunc, opts Options) Result {
	result := Result{Index: index, Name: scenario.Name}

	dir, err := os.MkdirTemp("", fmt.Sprintf("argent-eval-%02d-", index))
	if err != nil {
		result.Reason = "cannot create the scratch directory: " + err.Error()
		return result
	}
	if !opts.Keep {
		defer os.RemoveAll(dir)
	}
	if err := seed(dir, scenario.Files); err != nil {
		result.Reason = err.Error()
		return result
	}

	provider := NewScripted(scenario.Provider, scenario.Repeat)
	model := scenario.Model
	if model == "" {
		model = DefaultModel
	}
	workspace := Workspace{
		Dir:     dir,
		Prompt:  scenario.Prompt,
		System:  scenario.System,
		Model:   model,
		Chatter: provider,
	}

	trace, runErr := callRun(ctx, run, scenario, workspace)
	result.Tools = append([]string(nil), trace.Tools...)
	result.Text = trace.Text
	result.Stopped = trace.Stopped
	result.Turns = provider.Calls()

	observed := observation{calls: provider.Calls(), violations: provider.Violations()}
	switch {
	case runErr == nil:
	case errors.Is(runErr, context.Canceled), errors.Is(runErr, context.DeadlineExceeded):
		// A cancelled run is a stop, not a harness failure.
		trace.Stopped = true
		result.Stopped = true
	default:
		result.Reason = "runner error: " + runErr.Error()
	}
	if result.Reason == "" {
		result.Reason = evaluate(scenario, trace, observed, dir)
	}
	result.Passed = result.Reason == ""
	return result
}

// callRun turns a panicking runner into a failed scenario instead of a dead
// suite.
func callRun(ctx context.Context, run RunFunc, scenario Scenario, workspace Workspace) (trace Trace, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("runner panic: %v", recovered)
		}
	}()
	return run(ctx, scenario, workspace)
}

// seed writes the scenario's starting files into the workspace.
func seed(dir string, files map[string]string) error {
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		target := filepath.Join(dir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("seed %q: %w", path, err)
		}
		if err := os.WriteFile(target, []byte(files[path]), 0o644); err != nil {
			return fmt.Errorf("seed %q: %w", path, err)
		}
	}
	return nil
}

// observation is the harness-owned view of what the scripted provider saw.
type observation struct {
	calls      int
	violations []string
}

// evaluate grades one run and returns "" when every expectation holds.
func evaluate(scenario Scenario, trace Trace, observed observation, dir string) string {
	if len(observed.violations) > 0 {
		return observed.violations[0]
	}
	expect := scenario.Expect
	if expect.ToolCalls != nil && !equalStrings(expect.ToolCalls, trace.Tools) {
		return fmt.Sprintf("expected tools %v, ran %v", expect.ToolCalls, nilOr(trace.Tools))
	}
	for _, needle := range expect.TextContains {
		if !strings.Contains(trace.Text, needle) {
			return fmt.Sprintf("the answer does not contain %q (got %q)", needle, clip(trace.Text, 200))
		}
	}
	for _, needle := range expect.TextNotContains {
		if strings.Contains(trace.Text, needle) {
			return fmt.Sprintf("the answer must not contain %q (got %q)", needle, clip(trace.Text, 200))
		}
	}
	if expect.Turns != nil && *expect.Turns != observed.calls {
		return fmt.Sprintf("expected %d turns, the model was called %d times", *expect.Turns, observed.calls)
	}
	if expect.Stopped != nil && *expect.Stopped != trace.Stopped {
		return fmt.Sprintf("expected stopped=%t, got %t", *expect.Stopped, trace.Stopped)
	}
	return checkWorkspace(scenario.Check, dir)
}

// checkWorkspace verifies the seeded workspace after the run.
func checkWorkspace(check Check, dir string) string {
	for _, path := range check.FilesExist {
		target := filepath.Join(dir, filepath.FromSlash(path))
		info, err := os.Stat(target)
		if err != nil {
			return fmt.Sprintf("file %q does not exist after the run", path)
		}
		if info.IsDir() {
			return fmt.Sprintf("%q is a directory, not a file", path)
		}
	}
	paths := make([]string, 0, len(check.FileContains))
	for path := range check.FileContains {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		needle := check.FileContains[path]
		target := filepath.Join(dir, filepath.FromSlash(path))
		data, err := os.ReadFile(target)
		if err != nil {
			return fmt.Sprintf("cannot read %q after the run: %v", path, err)
		}
		if !strings.Contains(string(data), needle) {
			return fmt.Sprintf("file %q does not contain %q (got %q)", path, needle, clip(string(data), 200))
		}
	}
	return ""
}

func equalStrings(expected, actual []string) bool {
	if len(expected) != len(actual) {
		return false
	}
	for i := range expected {
		if expected[i] != actual[i] {
			return false
		}
	}
	return true
}

func nilOr(tools []string) []string {
	if tools == nil {
		return []string{}
	}
	return tools
}

// oneLine keeps a failure reason on one TAP line.
func oneLine(reason string) string {
	return strings.Join(strings.Fields(reason), " ")
}

// clip collapses whitespace and truncates a value for a report line.
func clip(text string, limit int) string {
	joined := strings.Join(strings.Fields(text), " ")
	if len(joined) <= limit {
		return joined
	}
	if limit <= 1 {
		return joined[:limit]
	}
	return joined[:limit-1] + "…"
}
