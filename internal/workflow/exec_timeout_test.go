package workflow

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestStepTimeoutFailsSeamThatIgnoresContext checks that a step-local timeout
// is enforced by the executor itself. The parent run context is still healthy
// after a per-attempt deadline, so runStep cannot detect this case; attempt
// must notice that the bound context expired even when the injected seam
// ignored it and returned a successful value.
func TestStepTimeoutFailsSeamThatIgnoresContext(t *testing.T) {
	t.Run("prompter", func(t *testing.T) {
		exec := &Executor{Prompter: prompterFunc(func(context.Context, string) (string, error) {
			time.Sleep(50 * time.Millisecond) // deliberately ignores ctx
			return "too late", nil
		})}
		def := mustDefinition(t, `{"steps":[{"name":"a","prompt":"a","timeout":"5ms"}]}`)

		started := time.Now()
		run, err := exec.Run(context.Background(), def, "")
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if elapsed := time.Since(started); elapsed > 2*time.Second {
			t.Fatalf("the timeout did not interrupt the step (%s)", elapsed)
		}
		if run.Status != StatusFailed {
			t.Fatalf("status = %q, want %q; step = %+v", run.Status, StatusFailed, run.Steps[0])
		}
		step := run.Steps[0]
		if step.Status != StatusFailed || step.Attempts != 1 {
			t.Fatalf("step = %+v", step)
		}
		if step.Output != "" {
			t.Fatalf("a late result must not be recorded as output: %q", step.Output)
		}
		if !strings.Contains(step.Error, "timed out after 5ms") {
			t.Fatalf("error = %q, want the step timeout", step.Error)
		}
	})

	t.Run("tool", func(t *testing.T) {
		exec := &Executor{Tools: toolsFunc(func(context.Context, string, string) (string, error) {
			time.Sleep(50 * time.Millisecond) // deliberately ignores ctx
			return "too late", nil
		})}
		def := mustDefinition(t, `{"steps":[{"name":"a","tool":"write","args":"x","timeout":"5ms"}]}`)

		run, err := exec.Run(context.Background(), def, "")
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if run.Status != StatusFailed {
			t.Fatalf("status = %q, want %q; step = %+v", run.Status, StatusFailed, run.Steps[0])
		}
		if step := run.Steps[0]; step.Output != "" || !strings.Contains(step.Error, "timed out after 5ms") {
			t.Fatalf("step = %+v", step)
		}
	})
}

// TestStepTimeoutKeepsFastSeamOK guards the other side of the deadline check:
// a seam that finishes inside its budget must not be marked as timed out.
func TestStepTimeoutKeepsFastSeamOK(t *testing.T) {
	exec := &Executor{Prompter: prompterFunc(func(context.Context, string) (string, error) {
		return "quick", nil
	})}
	def := mustDefinition(t, `{"steps":[{"name":"a","prompt":"a","timeout":"5s"}]}`)

	run, err := exec.Run(context.Background(), def, "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !run.Succeeded() || run.Steps[0].Output != "quick" || run.Steps[0].Attempts != 1 {
		t.Fatalf("run = %+v", run)
	}
}
