package cli

import (
	"context"
	"io"
	"strings"
	"testing"
)

// fakeHistoryRunner 记录 history 前缀的分派参数并模拟通过数/失败数。
type fakeHistoryRunner struct {
	called bool
	spec   HistoryEvalSpec
	passed int
	failed int
	err    error
}

func (f *fakeHistoryRunner) RunHistory(_ context.Context, spec HistoryEvalSpec, _ io.Writer) (int, int, error) {
	f.called = true
	f.spec = spec
	return f.passed, f.failed, f.err
}

// TestRunEvalHistoryDispatches 证明 history 是带外前缀：注入的 runner 收到仓库与选项，
// 全通过时退出 0。
func TestRunEvalHistoryDispatches(t *testing.T) {
	stdio, _, errOut := newTestIO("", nil)
	runner := &fakeHistoryRunner{passed: 3, failed: 0}
	code := RunWith(App{IO: stdio, Eval: runner}, []string{
		"eval", "history", "/repo", "--max-tasks", "7", "--since", "HEAD~5",
	})
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	if !runner.called {
		t.Fatal("history runner was not called")
	}
	if runner.spec.Repo != "/repo" || runner.spec.MaxTasks != 7 || runner.spec.Since != "HEAD~5" {
		t.Fatalf("spec = %+v, want repo=/repo max=7 since=HEAD~5", runner.spec)
	}
}

// TestRunEvalHistoryScripted 验证 --scripted 被解析并传给 runner，可离线驱动流程。
func TestRunEvalHistoryScripted(t *testing.T) {
	stdio, _, errOut := newTestIO("", nil)
	runner := &fakeHistoryRunner{passed: 0, failed: 1} // 脚本不修 bug，预期失败
	code := RunWith(App{IO: stdio, Eval: runner}, []string{
		"eval", "history", "/repo", "--scripted", "--max-tasks=3",
	})
	if code != 1 {
		t.Fatalf("exit = %d, stderr = %q, want 1 for failed scenario", code, errOut.String())
	}
	if !runner.called || !runner.spec.Scripted || runner.spec.MaxTasks != 3 {
		t.Fatalf("spec = %+v, want scripted with max=3", runner.spec)
	}
}

// TestRunEvalHistoryVerifyFlag 验证 --verify 被解析并传给 runner。
func TestRunEvalHistoryVerifyFlag(t *testing.T) {
	stdio, _, errOut := newTestIO("", nil)
	runner := &fakeHistoryRunner{passed: 2, failed: 0}
	code := RunWith(App{IO: stdio, Eval: runner}, []string{
		"eval", "history", "/repo", "--verify", "--scripted",
	})
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	if !runner.called || !runner.spec.Verify || !runner.spec.Scripted {
		t.Fatalf("spec = %+v, want verify and scripted", runner.spec)
	}
}

// TestRunEvalHistoryVerifyDispatch 验证 history verify 子前缀走离线自检路径，
// 不调用 app.Eval（不需要模型 runner）。
func TestRunEvalHistoryVerifyDispatch(t *testing.T) {
	stdio, _, errOut := newTestIO("", nil)
	// 无 runner 也应进入 verify 路径（它直接调用 eval 纯函数）；
	// 用不存在的仓库触发明确错误而非 panic。
	code := RunWith(App{IO: stdio}, []string{"eval", "history", "verify", "/nonexistent-repo-xyz"})
	if code == 0 {
		t.Fatalf("exit = 0, stderr = %q, want non-zero for missing repo", errOut.String())
	}
	if !strings.Contains(errOut.String(), "eval history verify:") {
		t.Fatalf("stderr = %q, want a verify diagnostic", errOut.String())
	}
}

// TestRunEvalHistoryArgValidation 验证缺仓库路径与非法参数时退出非 0 并给出中文提示。
func TestRunEvalHistoryArgValidation(t *testing.T) {
	cases := [][]string{
		{"eval", "history"},
		{"eval", "history", "/repo", "--max-tasks", "0"},
		{"eval", "history", "/repo", "--bogus"},
	}
	for _, args := range cases {
		stdio, _, errOut := newTestIO("", nil)
		runner := &fakeHistoryRunner{}
		code := RunWith(App{IO: stdio, Eval: runner}, args)
		if code == 0 {
			t.Errorf("args %v: exit = 0, want non-zero", args)
		}
		if runner.called {
			t.Errorf("args %v: runner must not be called on invalid input", args)
		}
		if !strings.Contains(errOut.String(), "eval history:") {
			t.Errorf("args %v: stderr = %q, want a diagnostic", args, errOut.String())
		}
	}
}

// TestEvalHistoryUsage 验证帮助文本包含子命令与选项。
func TestEvalHistoryUsage(t *testing.T) {
	stdio, out, _ := newTestIO("", nil)
	code := RunWith(App{IO: stdio}, []string{"eval", "history", "--help"})
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	got := out.String()
	for _, want := range []string{"eval history <repo>", "--max-tasks", "--since", "--scripted"} {
		if !strings.Contains(got, want) {
			t.Errorf("help missing %q:\n%s", want, got)
		}
	}
}
