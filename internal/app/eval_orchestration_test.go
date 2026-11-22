package app

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/cli"
)

// TestEvalOrchestrationCommandRunsRealSuite 端到端验证 `eval orchestration`：
// 真实 evaler 作为 cli.OrchestrationRunner 注入，跑 evals/orchestration.json 的 10 个场景，
// 输出 10/10 汇总并退出 0。
func TestEvalOrchestrationCommandRunsRealSuite(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test file")
	}
	path := filepath.Join(filepath.Dir(thisFile), "..", "..", "evals", "orchestration.json")
	stdio, out, errOut := newCLIAppIO()
	app := cli.App{IO: stdio, Eval: NewEvaler(stdio)}
	code := cli.RunWith(app, []string{"eval", "orchestration", path})
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q\nstdout:\n%s", code, errOut.String(), out.String())
	}
	got := out.String()
	for _, want := range []string{"Total: 10", "Passed: 10", "Failed: 0", "10/10 orchestration scenarios passed"} {
		if !strings.Contains(got, want) {
			t.Errorf("stdout missing %q:\n%s", want, got)
		}
	}
}

// TestEvalOrchestrationCommandFailingCopyExitsNonZero 注入一个必然失败的编排副本：
// 整条链（CLI 分派 → 接口断言 → app 包装 → eval 原语）把 Reason 打印出来并退出非 0。
func TestEvalOrchestrationCommandFailingCopyExitsNonZero(t *testing.T) {
	fixture := filepath.Join(t.TempDir(), "failing-orchestration.json")
	doc := `{"scenarios":[{"id":"X1","name":"失败副本","prompt":"Just answer.","turns":[{"target":"parent","respond":{"text":"done"}}],"assertions":[{"kind":"child_runs","equals":1}]}]}`
	if err := os.WriteFile(fixture, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	stdio, out, errOut := newCLIAppIO()
	app := cli.App{IO: stdio, Eval: NewEvaler(stdio)}
	code := cli.RunWith(app, []string{"eval", "orchestration", fixture})
	if code == 0 {
		t.Fatalf("exit = 0, want non-zero; stdout:\n%s", out.String())
	}
	got := out.String()
	for _, want := range []string{"Failed: 1", "X1", "失败副本", "child_runs = 0, want 1"} {
		if !strings.Contains(got, want) {
			t.Errorf("stdout missing %q:\n%s", want, got)
		}
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", errOut.String())
	}
}

// newCLIAppIO 为 cli.RunWith 构造带缓冲的 IO。
func newCLIAppIO() (cli.IO, *bytes.Buffer, *bytes.Buffer) {
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	return cli.IO{
		Out: out,
		Err: errOut,
		Env: func(string) string { return "" },
	}, out, errOut
}
