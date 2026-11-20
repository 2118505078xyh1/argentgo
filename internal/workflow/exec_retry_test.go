package workflow

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestRetriesDoNotCountCancelledBackoffAsAttempt checks that StepResult.Attempts
// counts real seam invocations. When a cancellation interrupts the backoff
// sleep before the next attempt, that upcoming attempt must not be counted —
// the step ran only once.
func TestRetriesDoNotCountCancelledBackoffAsAttempt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls int
	exec := &Executor{
		Backoff: 300 * time.Millisecond,
		Prompter: prompterFunc(func(context.Context, string) (string, error) {
			calls++
			if calls == 1 {
				// Cancel well inside the backoff window, after the first
				// attempt has already failed and runStep is waiting to retry.
				time.AfterFunc(20*time.Millisecond, cancel)
				return "", errors.New("first attempt failed")
			}
			return "second attempt", nil
		}),
	}
	def := mustDefinition(t, `{"steps":[{"name":"a","prompt":"a","retries":1}]}`)

	run, err := exec.Run(ctx, def, "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if run.Status != StatusFailed {
		t.Fatalf("status = %q, want %q", run.Status, StatusFailed)
	}
	if calls != 1 {
		t.Fatalf("seam called %d times, want 1 (the retry was cancelled)", calls)
	}
	step := run.Steps[0]
	if step.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1 (only one real invocation)", step.Attempts)
	}
	if step.Output != "" {
		t.Errorf("output = %q, want empty after a cancelled retry", step.Output)
	}
}

// TestRetriesCountSuccessfulBackoffAsAttempt is the counterpart: when the
// backoff completes, the retried attempt is a real invocation and is counted.
func TestRetriesCountSuccessfulBackoffAsAttempt(t *testing.T) {
	var calls int
	exec := &Executor{
		Backoff: time.Millisecond,
		Prompter: prompterFunc(func(context.Context, string) (string, error) {
			calls++
			if calls < 2 {
				return "", errors.New("flaky")
			}
			return "recovered", nil
		}),
	}
	def := mustDefinition(t, `{"steps":[{"name":"a","prompt":"a","retries":1}]}`)

	run, err := exec.Run(context.Background(), def, "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !run.Succeeded() {
		t.Fatalf("run = %+v", run)
	}
	if calls != 2 || run.Steps[0].Attempts != 2 {
		t.Fatalf("calls = %d, attempts = %d, want 2 and 2", calls, run.Steps[0].Attempts)
	}
}
