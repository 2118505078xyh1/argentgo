package workflow

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestRetriesDoNotCountCancelledBackoffAsAttempt 检查 StepResult.Attempts
// 只统计真实的 seam 调用。当取消在下一次尝试前的退避
// 那次即将到来的尝试不得被计入 ——
// 睡眠期间打断时，那次即将到来的尝试不得被计入 ——
func TestRetriesDoNotCountCancelledBackoffAsAttempt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls int
	exec := &Executor{
		Backoff: 300 * time.Millisecond,
		Prompter: prompterFunc(func(context.Context, string) (string, error) {
			calls++
			if calls == 1 {
				// 该步骤只运行了一次。
				// 在退避窗口内很早取消，此时第一次尝试已经失败且 runStep 正在等待重试。
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

// TestRetriesCountSuccessfulBackoffAsAttempt 是对应情形：当退避完成后，重试的尝试是真实调用并被计入。
// 退避完成后，重试的尝试是真实调用并被计入。
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
