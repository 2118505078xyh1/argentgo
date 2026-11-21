package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeLiveSuiteRunner 记录 --live 分派，并模拟真实模型跑批的结果。
type fakeLiveSuiteRunner struct {
	called bool
	path   string
	passed int
	failed int
	err    error
}

func (f *fakeLiveSuiteRunner) RunSuiteLive(_ context.Context, path string, _ io.Writer) (int, int, error) {
	f.called = true
	f.path = path
	return f.passed, f.failed, f.err
}

// fakeDualEvalRunner 同时实现套件接口与 live 接口，用于验证分派优先级。
type fakeDualEvalRunner struct {
	suite *fakeSuiteRunner
	live  *fakeLiveSuiteRunner
}

func (f *fakeDualEvalRunner) RunSuite(ctx context.Context, path string, out io.Writer) (int, int, error) {
	return f.suite.RunSuite(ctx, path, out)
}

func (f *fakeDualEvalRunner) RunSuiteLive(ctx context.Context, path string, out io.Writer) (int, int, error) {
	return f.live.RunSuiteLive(ctx, path, out)
}

func writeEvalFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "core.json")
	if err := os.WriteFile(path, []byte(`{"scenarios":[{"prompt":"hi"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunEvalLiveDispatchesToLiveRunner(t *testing.T) {
	path := writeEvalFixture(t)
	stdio, _, errOut := newTestIO("", nil)
	live := &fakeLiveSuiteRunner{passed: 2}
	code := RunWith(App{IO: stdio, Eval: live}, []string{"eval", "--live", path})
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	if !live.called || live.path != path {
		t.Fatalf("live called = %v, path = %q (want %q)", live.called, live.path, path)
	}
}

func TestRunEvalLivePrefersLiveOverSuite(t *testing.T) {
	path := writeEvalFixture(t)
	stdio, _, _ := newTestIO("", nil)
	dual := &fakeDualEvalRunner{suite: &fakeSuiteRunner{passed: 1}, live: &fakeLiveSuiteRunner{passed: 1}}
	code := RunWith(App{IO: stdio, Eval: dual}, []string{"eval", "--live", path})
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !dual.live.called {
		t.Fatal("live runner was not called")
	}
	if dual.suite.path != "" {
		t.Fatalf("scripted suite was also called with path %q", dual.suite.path)
	}
}

func TestRunEvalWithoutLiveUsesSuiteRunner(t *testing.T) {
	path := writeEvalFixture(t)
	stdio, out, _ := newTestIO("", nil)
	dual := &fakeDualEvalRunner{suite: &fakeSuiteRunner{passed: 1}, live: &fakeLiveSuiteRunner{passed: 1}}
	code := RunWith(App{IO: stdio, Eval: dual}, []string{"eval", path})
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if dual.suite.path != path {
		t.Fatalf("suite path = %q, want %q", dual.suite.path, path)
	}
	if dual.live.called {
		t.Fatal("live runner must not be called without --live")
	}
	if strings.Contains(out.String(), "ok   1") {
		t.Fatalf("simple grader ran instead of the suite:\n%s", out.String())
	}
}

func TestRunEvalLiveUnsupportedRunner(t *testing.T) {
	path := writeEvalFixture(t)
	stdio, _, errOut := newTestIO("", nil)
	code := RunWith(App{IO: stdio, Eval: &fakeSuiteRunner{passed: 1}}, []string{"eval", "--live", path})
	if code != 1 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "does not support --live") {
		t.Fatalf("stderr = %q", errOut.String())
	}
}

func TestRunEvalLiveFailurePropagates(t *testing.T) {
	path := writeEvalFixture(t)
	cases := []struct {
		name string
		live *fakeLiveSuiteRunner
	}{
		{name: "failed count", live: &fakeLiveSuiteRunner{passed: 1, failed: 1}},
		{name: "runner error", live: &fakeLiveSuiteRunner{err: errors.New("boom")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdio, _, _ := newTestIO("", nil)
			if code := RunWith(App{IO: stdio, Eval: tc.live}, []string{"eval", "--live", path}); code != 1 {
				t.Fatalf("exit = %d", code)
			}
		})
	}
}

func TestRunEvalRejectsOtherDashArgs(t *testing.T) {
	for _, args := range [][]string{
		{"eval", "--nope"},
		{"eval", "--live", "--nope"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			stdio, out, errOut := newTestIO("", nil)
			code := RunWith(App{IO: stdio, Eval: &fakeLiveSuiteRunner{}}, args)
			if code != 2 {
				t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
			}
			if out.Len() != 0 {
				t.Fatalf("stdout = %q", out.String())
			}
			if !strings.Contains(errOut.String(), "usage: argent eval") {
				t.Fatalf("stderr = %q", errOut.String())
			}
		})
	}
}

func TestRunEvalLiveRequiresPath(t *testing.T) {
	stdio, out, errOut := newTestIO("", nil)
	code := RunWith(App{IO: stdio, Eval: &fakeLiveSuiteRunner{}}, []string{"eval", "--live"})
	if code != 2 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	if out.Len() != 0 {
		t.Fatalf("stdout = %q", out.String())
	}
	if !strings.Contains(errOut.String(), "usage: argent eval") {
		t.Fatalf("stderr = %q", errOut.String())
	}
}

func TestEvalUsageMentionsLive(t *testing.T) {
	var out strings.Builder
	evalUsage(&out)
	if !strings.Contains(out.String(), "--live") {
		t.Fatalf("eval usage does not mention --live:\n%s", out.String())
	}
}
