package workflow

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// TestFailedCheckAddsRetryHintWhenRetriesConfigured checks that a failed
// StepCheck carries a bounded, redacted verdict and a next-step nudge when the
// step will be retried.
func TestFailedCheckAddsRetryHintWhenRetriesConfigured(t *testing.T) {
	requireWorkflowShell(t)
	exec := &Executor{
		Backoff:  time.Millisecond,
		Prompter: prompterFunc(func(context.Context, string) (string, error) { return "done", nil }),
	}
	def := mustDefinition(t, `{"steps":[{"name":"verify","prompt":"a","retries":1,
		"success_check":{"command":"echo CHECK_FAIL_MARKER 1>&2; exit 1"}}]}`)

	run, err := exec.Run(context.Background(), def, "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if run.Status != StatusFailed {
		t.Fatalf("status = %q, run = %+v", run.Status, run)
	}
	step := run.Steps[0]
	if step.Attempts != 2 {
		t.Fatalf("attempts = %d, want 2 (one retry)", step.Attempts)
	}
	for _, want := range []string{
		"CHECK_FAIL_MARKER",
		"success check",
		"[verify: VERIFY] FAIL",
		"do not re-run the same check unchanged; adjust the code first.",
	} {
		if !strings.Contains(step.Error, want) {
			t.Errorf("error = %q, want it to contain %q", step.Error, want)
		}
	}
}

// TestFailedCheckWithoutRetriesHasNoHint checks that a step with no retry
// budget keeps its failure diagnostic unchanged.
func TestFailedCheckWithoutRetriesHasNoHint(t *testing.T) {
	requireWorkflowShell(t)
	exec := &Executor{Prompter: prompterFunc(func(context.Context, string) (string, error) { return "done", nil })}
	def := mustDefinition(t, `{"steps":[{"name":"a","prompt":"a",
		"success_check":{"command":"echo CHECK_FAIL_MARKER 1>&2; exit 1"}}]}`)

	run, err := exec.Run(context.Background(), def, "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	step := run.Steps[0]
	if step.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1", step.Attempts)
	}
	if !strings.Contains(step.Error, "CHECK_FAIL_MARKER") {
		t.Fatalf("error = %q, want the original diagnostic", step.Error)
	}
	if strings.Contains(step.Error, "do not re-run") {
		t.Fatalf("error = %q, want no retry hint without a retry budget", step.Error)
	}
}

// TestRunSuccessCheckMarksFailureForRetryHint checks that a check failure is
// recognisable by runStep without changing its error text or unwrap chain.
func TestRunSuccessCheckMarksFailureForRetryHint(t *testing.T) {
	requireWorkflowShell(t)
	err := runSuccessCheck(context.Background(), Step{
		Name:         "a",
		SuccessCheck: &StepCheck{Command: "echo CHECK_MARK 1>&2; exit 1"},
	})
	var failed *checkFailure
	if !errors.As(err, &failed) {
		t.Fatalf("error = %v, want a *checkFailure", err)
	}
	if failed.output != "CHECK_MARK" {
		t.Fatalf("captured output = %q, want %q", failed.output, "CHECK_MARK")
	}
	if !strings.Contains(failed.Error(), "success check") {
		t.Fatalf("error text changed: %q", failed.Error())
	}
}

// TestCheckFailureKeepsDeadlineExceededChain checks that wrapping a check
// failure does not hide the underlying context error from callers.
func TestCheckFailureKeepsDeadlineExceededChain(t *testing.T) {
	cause := context.DeadlineExceeded
	failed := &checkFailure{output: "boom", cause: cause}
	if !errors.Is(failed, cause) {
		t.Fatal("checkFailure lost the cause chain")
	}
	if failed.Error() != cause.Error() {
		t.Fatalf("Error() = %q, want %q", failed.Error(), cause.Error())
	}
}

// TestCheckRetryHintRedactsAndBoundsOutput checks that the hint never leaks a
// credential, never truncates inside a rune and stays within its byte budget.
func TestCheckRetryHintRedactsAndBoundsOutput(t *testing.T) {
	out := strings.Repeat("界x", 400) + "api_key=sk_live_abcdefghijklmnop1234 FAIL_TAIL"
	got := checkRetryHint(Step{Name: " build "}, out)

	if strings.Contains(got, "sk_live_abcdefghijklmnop1234") {
		t.Fatalf("hint leaked the secret: %q", got)
	}
	if !strings.Contains(got, "[redacted]") {
		t.Fatalf("hint = %q, want a [redacted] marker", got)
	}
	if !strings.HasPrefix(got, "[verify: BUILD] FAIL\n") {
		t.Fatalf("hint = %q, want an upper-cased verdict line", got)
	}
	if !strings.HasSuffix(got, "\ndo not re-run the same check unchanged; adjust the code first.") {
		t.Fatalf("hint = %q, want the next-step instruction last", got)
	}
	lines := strings.Split(got, "\n")
	if len(lines) < 3 {
		t.Fatalf("hint = %q, want verdict, output and instruction lines", got)
	}
	if len(lines[1]) > maxCheckAnchorOutput {
		t.Fatalf("output line = %d bytes, want <= %d", len(lines[1]), maxCheckAnchorOutput)
	}
	if !utf8.ValidString(got) || !strings.HasSuffix(lines[1], "FAIL_TAIL") {
		t.Fatalf("output line = %q, want a rune-safe tail", lines[1])
	}
}
