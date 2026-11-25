package app

import (
	"bytes"
	"context"
	"io"
	"math"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"example.com/argentgo/internal/cli"
	"example.com/argentgo/internal/eval"
)

func TestEvalSuiteRunsCoreScenariosThroughTheRealLoop(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test file")
	}
	path := filepath.Join(filepath.Dir(thisFile), "..", "..", "evals", "core.json")
	var out bytes.Buffer
	runner := NewEvaler(cli.IO{Out: &out, Err: &out, Env: func(string) string { return "" }})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	passed, failed, err := runner.(*evaler).RunSuite(ctx, path, &out)
	if err != nil || failed != 0 {
		t.Fatalf("RunSuite = %d passed, %d failed, %v\n%s", passed, failed, err, out.String())
	}
	if passed != 11 {
		t.Fatalf("passed = %d, want 11\n%s", passed, out.String())
	}
	for _, want := range []string{"1..11", "ok 1 - answers a simple question", "ok 8 - the turn limit ends a run", "ok 11 - approval denial returns a readable final error"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("report misses %q:\n%s", want, out.String())
		}
	}
}

// TestApprovalDenialScenarioReadableErrorAndCompletion 单独验证 core.json 的审批否决场景：
// 真实 evaler 下脚本化 provider 能读到安全链回注的可读拒绝文本、文件未被改写、循环正常收尾，
// 且该场景被计入完成率。场景自身的 expect_request_contains / turns / stopped / file_equals
// 分别守护这四点；本测试再锁死"approve:false + 种子内容"的定义，避免被改成假阳性。
func TestApprovalDenialScenarioReadableErrorAndCompletion(t *testing.T) {
	const name = "approval denial returns a readable final error"
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test file")
	}
	path := filepath.Join(filepath.Dir(thisFile), "..", "..", "evals", "core.json")
	scenarios, err := eval.Load(path)
	if err != nil {
		t.Fatalf("Load(core.json): %v", err)
	}
	idx := -1
	for i := range scenarios {
		if scenarios[i].Name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatalf("core.json has no scenario %q", name)
	}
	scenario := scenarios[idx]
	if scenario.Approve == nil || *scenario.Approve {
		t.Fatal("scenario must set approve:false")
	}
	if got := scenario.Files["out.txt"]; got != "original\n" {
		t.Fatalf("seed out.txt = %q, want %q", got, "original\n")
	}
	if got := scenario.Check.FileEquals["out.txt"]; got != "original\n" {
		t.Fatalf("check.file_equals[out.txt] = %q, want %q", got, "original\n")
	}

	runner := NewEvaler(cli.IO{Out: io.Discard, Err: io.Discard, Env: func(string) string { return "" }})
	var out bytes.Buffer
	report, err := eval.Run(
		context.Background(),
		[]eval.Scenario{scenario},
		runner.(*evaler).runScenario,
		eval.Options{Out: &out},
	)
	if err != nil {
		t.Fatalf("Run(approval denial): %v\n%s", err, out.String())
	}
	if report.Passed != 1 || report.Failed != 0 {
		t.Fatalf("report = %d passed, %d failed\n%s", report.Passed, report.Failed, out.String())
	}
	if rate := eval.ScenarioCompletionRate(report.Results); math.Abs(rate-1.0) > 1e-9 {
		t.Fatalf("completion rate = %v, want 1.0", rate)
	}
}
