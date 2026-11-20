package workflow

import (
	"context"
	"strings"
	"testing"
	"time"
)

// 调用方的定义必须保持不变。
// TestStepTimeoutFailsSeamThatIgnoresContext 检查步骤局部超时由 executor 自身强制执行。在每次尝试的截止时间之后，父运行 context 仍然健康，
// 在每次尝试的截止时间之后，runStep 无法检测这种情况；attempt
// 因此 runStep 无法检测这种情况；attempt 必须注意到绑定 context 已过期，即使注入的 seam
// 忽略了它并返回了成功的值。
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

// TestStepTimeoutKeepsFastSeamOK 守护截止时间检查的另一面：
// 在其预算内完成的 seam 不得被标记为超时。
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
