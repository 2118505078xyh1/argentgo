package cli

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// fakeOrchestrationRunner 记录 orchestration 前缀的分派，并模拟一份编排报告。
type fakeOrchestrationRunner struct {
	called bool
	path   string
	report OrchestrationReport
	err    error
}

func (f *fakeOrchestrationRunner) RunOrchestration(_ context.Context, path string) (OrchestrationReport, error) {
	f.called = true
	f.path = path
	return f.report, f.err
}

// TestRunEvalOrchestrationDispatchesAndPrintsSummary 证明 orchestration 是带外前缀：
// 注入的 OrchestrationRunner 收到文件路径，输出 Total/Passed/Failed 与 10/10 汇总，退出 0。
func TestRunEvalOrchestrationDispatchesAndPrintsSummary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "orchestration.json")
	stdio, out, errOut := newTestIO("", nil)
	runner := &fakeOrchestrationRunner{report: OrchestrationReport{Total: 10, Passed: 10, Failed: 0}}
	code := RunWith(App{IO: stdio, Eval: runner}, []string{"eval", "orchestration", path})
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	if !runner.called || runner.path != path {
		t.Fatalf("called = %v, path = %q (want %q)", runner.called, runner.path, path)
	}
	got := out.String()
	for _, want := range []string{
		"Total: 10", "Passed: 10", "Failed: 0", "10/10 orchestration scenarios passed",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("stdout missing %q:\n%s", want, got)
		}
	}
}

// TestRunEvalOrchestrationFailurePrintsReasonAndExitsNonZero 证明任一场景失败时退出非 0，
// 并逐条打印 OrchestrationFailure 的 ID/Name/Reason。
func TestRunEvalOrchestrationFailurePrintsReasonAndExitsNonZero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "orchestration.json")
	stdio, out, errOut := newTestIO("", nil)
	runner := &fakeOrchestrationRunner{report: OrchestrationReport{
		Total:  2,
		Passed: 1,
		Failed: 1,
		Failures: []OrchestrationFailure{{
			ID: "S3a", Name: "委派重试", Reason: "child_runs = 1, want 2",
		}},
	}}
	code := RunWith(App{IO: stdio, Eval: runner}, []string{"eval", "orchestration", path})
	if code == 0 {
		t.Fatalf("exit = 0, want non-zero; stdout = %q", out.String())
	}
	got := out.String()
	for _, want := range []string{
		"Total: 2", "Passed: 1", "Failed: 1",
		"S3a", "委派重试", "child_runs = 1, want 2",
		"1/2 orchestration scenarios passed",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("stdout missing %q:\n%s", want, got)
		}
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", errOut.String())
	}
}

// TestRunEvalOrchestrationRunnerErrorExitsNonZero 覆盖加载/运行出错的路径。
func TestRunEvalOrchestrationRunnerErrorExitsNonZero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")
	stdio, out, errOut := newTestIO("", nil)
	runner := &fakeOrchestrationRunner{err: errors.New("load failed")}
	code := RunWith(App{IO: stdio, Eval: runner}, []string{"eval", "orchestration", path})
	if code != 1 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	if out.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", out.String())
	}
	if !strings.Contains(errOut.String(), "load failed") {
		t.Fatalf("stderr = %q", errOut.String())
	}
}

// TestRunEvalOrchestrationUnsupportedRunner 证明 runner 未实现窄接口时给出干净错误。
func TestRunEvalOrchestrationUnsupportedRunner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "orchestration.json")
	stdio, _, errOut := newTestIO("", nil)
	code := RunWith(App{IO: stdio, Eval: &fakeSuiteRunner{}}, []string{"eval", "orchestration", path})
	if code != 1 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "does not support orchestration") {
		t.Fatalf("stderr = %q", errOut.String())
	}
}

// TestRunEvalOrchestrationHelp 验证 `eval orchestration -h` 不需要文件，打印用法。
func TestRunEvalOrchestrationHelp(t *testing.T) {
	stdio, out, errOut := newTestIO("", nil)
	code := RunWith(App{IO: stdio}, []string{"eval", "orchestration", "-h"})
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "usage: argentgo eval orchestration") {
		t.Fatalf("stdout missing orchestration usage:\n%s", out.String())
	}
}

// TestRunEvalOrchestrationRequiresPath 覆盖缺少/以连字符开头的路径。
func TestRunEvalOrchestrationRequiresPath(t *testing.T) {
	for _, args := range [][]string{
		{"eval", "orchestration"},
		{"eval", "orchestration", "--nope"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			stdio, out, errOut := newTestIO("", nil)
			code := RunWith(App{IO: stdio, Eval: &fakeOrchestrationRunner{}}, args)
			if code != 2 {
				t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
			}
			if out.Len() != 0 {
				t.Fatalf("stdout = %q", out.String())
			}
			if !strings.Contains(errOut.String(), "usage: argentgo eval orchestration") {
				t.Fatalf("stderr = %q", errOut.String())
			}
		})
	}
}

// TestEvalUsageMentionsOrchestration 确保主 eval 用法提示包含 orchestration 前缀。
func TestEvalUsageMentionsOrchestration(t *testing.T) {
	var out strings.Builder
	evalUsage(&out)
	if !strings.Contains(out.String(), "eval orchestration") {
		t.Fatalf("eval usage does not mention orchestration:\n%s", out.String())
	}
}
