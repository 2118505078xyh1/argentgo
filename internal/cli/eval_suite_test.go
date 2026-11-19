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

type fakeSuiteRunner struct {
	path   string
	passed int
	failed int
	err    error
}

func (f *fakeSuiteRunner) RunSuite(_ context.Context, path string, _ io.Writer) (int, int, error) {
	f.path = path
	return f.passed, f.failed, f.err
}

func TestRunEvalPrefersTheScriptedSuiteRunner(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "core.json")
	if err := os.WriteFile(path, []byte(`{"scenarios":[{"prompt":"hi"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	stdio, out, errOut := newTestIO("", nil)
	suite := &fakeSuiteRunner{passed: 1}
	code := RunWith(App{IO: stdio, Eval: suite}, []string{"eval", path})
	if code != 0 || suite.path != path {
		t.Fatalf("exit = %d, path = %q, stderr = %q", code, suite.path, errOut.String())
	}
	if strings.Contains(out.String(), "ok   1") {
		t.Fatalf("simple grader ran instead of the suite:\n%s", out.String())
	}
}

func TestRunEvalReturnsFailureFromSuiteRunner(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "core.json")
	if err := os.WriteFile(path, []byte(`{"scenarios":[{"prompt":"hi"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	stdio, _, errOut := newTestIO("", nil)
	suite := &fakeSuiteRunner{passed: 1, failed: 1, err: errors.New("eval: 1 of 2 scenarios failed")}
	if code := RunWith(App{IO: stdio, Eval: suite}, []string{"eval", path}); code != 1 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
}
