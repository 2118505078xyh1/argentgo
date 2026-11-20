package workflow

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/proc"
)

// requireWorkflowShell 在没有可用 shell 运行客观检查与清理命令时跳过测试。
// 运行客观检查与清理命令。
func requireWorkflowShell(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := proc.Shell("exit 0")
	cmd.Timeout = 2 * time.Second
	if _, err := proc.Run(ctx, cmd); err != nil {
		t.Skipf("workflow shell is unavailable: %v", err)
	}
}

// shellSupports 报告 shell 能否成功运行命令。
func shellSupports(command string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := proc.Shell(command)
	cmd.Timeout = time.Second
	_, err := proc.Run(ctx, cmd)
	return err == nil
}

func TestSuccessCheckFieldsParseAsOptional(t *testing.T) {
	def := mustDefinition(t, `{"steps":[
		{"name":"plain","prompt":"a"},
		{"name":"checked","prompt":"b","success_check":{"command":"exit 0","timeout_seconds":5},"cleanup":"echo bye"}
	]}`)
	if def.Steps[0].SuccessCheck != nil || def.Steps[0].CleanupCommand != "" {
		t.Fatalf("a step without the new fields changed: %+v", def.Steps[0])
	}
	check := def.Steps[1].SuccessCheck
	if check == nil || check.Command != "exit 0" || check.TimeoutSeconds != 5 {
		t.Fatalf("success_check = %+v", check)
	}
	if def.Steps[1].CleanupCommand != "echo bye" {
		t.Fatalf("cleanup = %q", def.Steps[1].CleanupCommand)
	}
}

func TestSuccessCheckPassesKeepsStepOK(t *testing.T) {
	requireWorkflowShell(t)
	rec := &recorder{}
	exec := &Executor{Prompter: rec.prompter(func(string) (string, error) { return "done", nil })}
	def := mustDefinition(t, `{"steps":[{"name":"a","prompt":"a","success_check":{"command":"exit 0"}}]}`)

	run, err := exec.Run(context.Background(), def, "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !run.Succeeded() {
		t.Fatalf("run = %+v", run)
	}
	if got := run.Steps[0]; got.Attempts != 1 || got.Output != "done" || got.Error != "" {
		t.Fatalf("step = %+v", got)
	}
}

func TestSuccessCheckFailureRetriesAndReportsStderrTail(t *testing.T) {
	requireWorkflowShell(t)
	rec := &recorder{}
	exec := &Executor{Backoff: time.Millisecond, Prompter: rec.prompter(func(string) (string, error) { return "done", nil })}
	def := mustDefinition(t, `{"steps":[
		{"name":"a","prompt":"a","retries":2,"success_check":{"command":"echo CHECK_TAIL_MARKER 1>&2; exit 1"}},
		{"name":"b","prompt":"b"}
	]}`)

	run, err := exec.Run(context.Background(), def, "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if run.Status != StatusFailed {
		t.Fatalf("status = %q", run.Status)
	}
	step := run.Steps[0]
	if step.Status != StatusFailed || step.Attempts != 3 {
		t.Fatalf("step = %+v", step)
	}
	if !strings.Contains(step.Error, "CHECK_TAIL_MARKER") || !strings.Contains(step.Error, "success check") {
		t.Fatalf("error = %q", step.Error)
	}
	if run.Steps[1].Status != StatusSkipped {
		t.Fatalf("the default on_failure should stop the run: %+v", run.Steps[1])
	}
}

func TestDiagnosticTailCapsOutputAndKeepsUTF8Whole(t *testing.T) {
	s := strings.Repeat("界x", maxCheckDiagnostic/4) + "x" // len = max+1, cut lands mid-rune
	got := diagnosticTail(s, maxCheckDiagnostic)
	if len(got) > maxCheckDiagnostic || !utf8.ValidString(got) || !strings.HasSuffix(got, "x") {
		t.Fatalf("diagnosticTail returned %d bytes, valid utf8 = %v", len(got), utf8.ValidString(got))
	}
	if shorter := diagnosticTail("short", maxCheckDiagnostic); shorter != "short" {
		t.Fatalf("short diagnostic changed: %q", shorter)
	}
}

func TestCleanupCommandOutputAppearsInFailureDiagnostic(t *testing.T) {
	requireWorkflowShell(t)
	exec := &Executor{Prompter: prompterFunc(func(context.Context, string) (string, error) { return "", errors.New("primary boom") })}
	def := mustDefinition(t, `{"steps":[
		{"name":"a","prompt":"a","cleanup":"echo CLEANUP_MARKER && exit 1"},
		{"name":"b","prompt":"b"}
	]}`)

	run, err := exec.Run(context.Background(), def, "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	step := run.Steps[0]
	if step.Status != StatusFailed || !strings.Contains(step.Error, "primary boom") {
		t.Fatalf("step = %+v", step)
	}
	if !strings.Contains(step.Error, "cleanup") || !strings.Contains(step.Error, "CLEANUP_MARKER") {
		t.Fatalf("cleanup output missing from error = %q", step.Error)
	}
	if run.Steps[1].Status != StatusSkipped {
		t.Fatalf("cleanup exit code must not change the on_failure decision: %+v", run.Steps[1])
	}
}

func TestCleanupRunsAfterFailedSuccessCheck(t *testing.T) {
	requireWorkflowShell(t)
	exec := &Executor{Prompter: prompterFunc(func(context.Context, string) (string, error) { return "done", nil })}
	def := mustDefinition(t, `{"steps":[{"name":"a","prompt":"a",
		"success_check":{"command":"echo CHECK_MARKER 1>&2; exit 1"},
		"cleanup":"echo CLEANUP_AFTER_CHECK"}]}`)

	run, err := exec.Run(context.Background(), def, "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	step := run.Steps[0]
	if step.Status != StatusFailed {
		t.Fatalf("step = %+v", step)
	}
	if !strings.Contains(step.Error, "CHECK_MARKER") || !strings.Contains(step.Error, "CLEANUP_AFTER_CHECK") {
		t.Fatalf("error = %q", step.Error)
	}
}

func TestWithoutSuccessCheckBehaviorUnchanged(t *testing.T) {
	rec := &recorder{}
	exec := &Executor{Prompter: rec.prompter(func(string) (string, error) { return "done", nil })}
	def := mustDefinition(t, `{"steps":[{"name":"a","prompt":"a"}]}`)

	run, err := exec.Run(context.Background(), def, "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !run.Succeeded() || run.Steps[0].Output != "done" || run.Steps[0].Attempts != 1 {
		t.Fatalf("run = %+v", run)
	}
	if rec.promptCount() != 1 {
		t.Fatalf("prompter called %d times", rec.promptCount())
	}
}

func TestSuccessCheckTimeoutSecondsIsRespected(t *testing.T) {
	// shellSupports 也证明 shell 可用，因此这里无需额外探测。
	if !shellSupports("sleep 0") {
		t.Skip("the workflow shell does not support sleep")
	}
	exec := &Executor{Prompter: prompterFunc(func(context.Context, string) (string, error) { return "done", nil })}
	def := mustDefinition(t, `{"steps":[{"name":"a","prompt":"a","success_check":{"command":"sleep 3","timeout_seconds":1}}]}`)

	started := time.Now()
	run, err := exec.Run(context.Background(), def, "")
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("the check timeout did not interrupt the command (%s)", elapsed)
	}
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if run.Status != StatusFailed || !strings.Contains(run.Steps[0].Error, "timed out after 1s") {
		t.Fatalf("step = %+v", run.Steps[0])
	}
}

func TestStepTimeoutBoundsSuccessCheck(t *testing.T) {
	if !shellSupports("sleep 0") {
		t.Skip("the workflow shell does not support sleep")
	}
	exec := &Executor{Prompter: prompterFunc(func(context.Context, string) (string, error) { return "done", nil })}
	// 检查省略了 timeout_seconds（因此会得到 30s 默认值），但成功检查是 attempt 的一部分且仍必须遵守步骤预算。
	// 成功检查是 attempt 的一部分且仍必须遵守步骤预算。
	def := mustDefinition(t, `{"steps":[{"name":"a","prompt":"a","timeout":"50ms","success_check":{"command":"sleep 1"}}]}`)

	started := time.Now()
	run, err := exec.Run(context.Background(), def, "")
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if elapsed > time.Second {
		t.Fatalf("step timeout did not bound the success check (%s)", elapsed)
	}
	if run.Status != StatusFailed {
		t.Fatalf("status = %q, step = %+v", run.Status, run.Steps[0])
	}
	step := run.Steps[0]
	if step.Status != StatusFailed || !strings.Contains(step.Error, "success check timed out") {
		t.Fatalf("step = %+v", step)
	}
}

func TestSuccessCheckTimeoutKeepsDeadlineExceededChain(t *testing.T) {
	if !shellSupports("sleep 0") {
		t.Skip("the workflow shell does not support sleep")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	step := Step{Name: "a", SuccessCheck: &StepCheck{Command: "sleep 1"}}

	err := runSuccessCheck(ctx, step)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("success check lost the DeadlineExceeded chain: %v", err)
	}
}

func TestTimedOutWrapsDeadlineExceeded(t *testing.T) {
	err := timedOut(Step{Name: "a"}, context.DeadlineExceeded, 20*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timedOut lost the DeadlineExceeded chain: %v", err)
	}
	if !strings.Contains(err.Error(), "timed out after 20ms") {
		t.Fatalf("error = %q", err.Error())
	}
}
