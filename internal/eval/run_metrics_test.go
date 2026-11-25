package eval

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"example.com/argentgo/internal/llm"
)

// 本文件锁定 eval.Run 的输出契约：默认路径逐字节不变，Live 开关只在结尾追加指标行。
// 所有场景都是脚本化的固定场景，不依赖网络、时钟或随机性。

// byteExactScenarios 返回一组固定的、确定性的场景：第一个通过并上报用量，
// 第二个因文本不匹配失败，也上报另一种用量，便于同时验收完成率与 token 汇总。
func byteExactScenarios() []Scenario {
	return []Scenario{
		{
			Name:     "pass",
			Prompt:   "answer",
			Provider: []ScriptTurn{{Respond: Respond{Text: "ok", Usage: &llm.Usage{PromptTokens: 10, CompletionTokens: 2}}}},
			Expect:   Expect{TextContains: []string{"ok"}, Turns: intPtr(1)},
		},
		{
			Name:     "fail",
			Prompt:   "answer",
			Provider: []ScriptTurn{{Respond: Respond{Text: "ok", Usage: &llm.Usage{PromptTokens: 5, CompletionTokens: 3, CachedTokens: 1}}}},
			Expect:   Expect{TextContains: []string{"never"}, Turns: intPtr(1)},
		},
	}
}

func TestRunDefaultOutputIsByteExact(t *testing.T) {
	scenarios := byteExactScenarios()
	var out bytes.Buffer
	report, err := Run(context.Background(), scenarios, fakeRun, Options{Out: &out})
	if err == nil {
		t.Fatal("a suite with one failing scenario must return an error")
	}
	if report.Passed != 1 || report.Failed != 1 {
		t.Fatalf("report = %d passed, %d failed", report.Passed, report.Failed)
	}
	const want = "1..2\n" +
		"ok 1 - pass\n" +
		"not ok 2 - fail: the answer does not contain \"never\" (got \"ok\")\n" +
		"# 1 passed, 1 failed\n"
	if got := out.String(); got != want {
		t.Fatalf("default output drifted:\n got: %q\nwant: %q", got, want)
	}
	// Trace.Usage 必须被逐项拷贝进 Result，而不是在评分后丢弃。
	wantUsage := llm.Usage{PromptTokens: 10, CompletionTokens: 2}
	if got := report.Results[0].Usage; got != wantUsage {
		t.Fatalf("result[0].Usage = %+v, want %+v", got, wantUsage)
	}
	wantUsage = llm.Usage{PromptTokens: 5, CompletionTokens: 3, CachedTokens: 1}
	if got := report.Results[1].Usage; got != wantUsage {
		t.Fatalf("result[1].Usage = %+v, want %+v", got, wantUsage)
	}
}

func TestRunLiveAppendsCompletionRateAndTokenAndTurnSummaries(t *testing.T) {
	scenarios := byteExactScenarios()
	var out bytes.Buffer
	report, err := Run(context.Background(), scenarios, fakeRun, Options{Out: &out, Live: true})
	if err == nil {
		t.Fatal("a suite with one failing scenario must return an error")
	}
	if report.Passed != 1 || report.Failed != 1 {
		t.Fatalf("report = %d passed, %d failed", report.Passed, report.Failed)
	}
	// 完成率 1/2=0.5；token 累加 10+5=15、2+3=5、缓存 0+1=1、写入 0，总计 20；轮次 1+1=2。
	// 既有六行逐字节不变；追加的逐场景行与总耗时在下面单独断言（duration 是实测值，不固定）。
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	wantPrefix := []string{
		"1..2",
		"ok 1 - pass",
		`not ok 2 - fail: the answer does not contain "never" (got "ok")`,
		"# 1 passed, 1 failed",
		"# completion rate: 0.500",
		"# tokens: prompt=15 completion=5 cached=1 cache_write=0 total=20",
		"# turns: 2",
	}
	if len(lines) < len(wantPrefix)+3 {
		t.Fatalf("live output has too few lines:\n%s", out.String())
	}
	for i, want := range wantPrefix {
		if lines[i] != want {
			t.Errorf("line %d = %q, want %q", i+1, lines[i], want)
		}
	}
	wantScenarioPrefixes := []string{
		"# scenario 1: tokens prompt=10 completion=2 cached=0 cache_write=0 total=12 duration=",
		"# scenario 2: tokens prompt=5 completion=3 cached=1 cache_write=0 total=8 duration=",
	}
	for i, prefix := range wantScenarioPrefixes {
		line := lines[len(wantPrefix)+i]
		if !strings.HasPrefix(line, prefix) {
			t.Errorf("scenario line %d = %q, want prefix %q", i+1, line, prefix)
			continue
		}
		if _, err := time.ParseDuration(strings.TrimPrefix(line, prefix)); err != nil {
			t.Errorf("scenario line %d has invalid duration: %q", i+1, line)
		}
	}
	totalLine := lines[len(lines)-1]
	if !strings.HasPrefix(totalLine, "# total duration: ") {
		t.Errorf("last line = %q, want total duration line", totalLine)
	} else if _, err := time.ParseDuration(strings.TrimPrefix(totalLine, "# total duration: ")); err != nil {
		t.Errorf("total duration line has invalid duration: %q", totalLine)
	}
}

func TestScenarioCompletionRateGuardsEmptyInput(t *testing.T) {
	for _, results := range [][]Result{nil, {}} {
		got := ScenarioCompletionRate(results)
		if got != 0 || math.IsNaN(got) {
			t.Fatalf("ScenarioCompletionRate(%v) = %v, want 0 without NaN", results, got)
		}
	}
	if got := ScenarioCompletionRate([]Result{{Passed: false}, {Passed: false}}); got != 0 {
		t.Fatalf("all-failing rate = %v, want 0", got)
	}
	if got := ScenarioCompletionRate([]Result{{Passed: true}, {Passed: true}}); got != 1 {
		t.Fatalf("all-passing rate = %v, want 1", got)
	}
}

func TestLiveMetricsReportIsPureAndDeterministic(t *testing.T) {
	const fakeKey = "sk-should-never-appear-in-metrics"
	results := []Result{
		{
			Name:   "通过 " + fakeKey,
			Passed: true,
			Turns:  2,
			Usage:  llm.Usage{PromptTokens: 7, CompletionTokens: 3, CachedTokens: 1, CacheWriteTokens: 2},
		},
		{Name: "失败", Passed: false, Turns: 1},
	}
	before := fmt.Sprintf("%+v", results)
	first := liveMetricsReport(results)
	if second := liveMetricsReport(results); second != first {
		t.Fatalf("liveMetricsReport is not deterministic:\nfirst:  %q\nsecond: %q", first, second)
	}
	if got := fmt.Sprintf("%+v", results); got != before {
		t.Fatalf("liveMetricsReport mutated its input:\nbefore: %q\nafter:  %q", before, got)
	}
	if strings.Contains(first, fakeKey) {
		t.Fatalf("metrics leaked a key-like token: %q", first)
	}
	if !strings.Contains(first, "# completion rate: 0.500\n") ||
		!strings.Contains(first, "# tokens: prompt=7 completion=3 cached=1 cache_write=2 total=10\n") ||
		!strings.Contains(first, "# turns: 3\n") {
		t.Fatalf("unexpected metrics:\n%s", first)
	}
	// 追加的逐场景行只用序号，不含场景名里的 key-like 文本；未设置 Duration 时为 0s。
	if !strings.Contains(first, "# scenario 1: tokens prompt=7 completion=3 cached=1 cache_write=2 total=10 duration=0s\n") ||
		!strings.Contains(first, "# scenario 2: tokens prompt=0 completion=0 cached=0 cache_write=0 total=0 duration=0s\n") ||
		!strings.Contains(first, "# total duration: 0s\n") {
		t.Fatalf("unexpected appended metric lines:\n%s", first)
	}
}

// TestLiveMetricsReportAppendsPerScenarioAndTotalDuration 用显式 Duration 锁定追加行的
// 精确格式：逐场景 token 细分 + 耗时，以及末行总耗时；既有三行保持原样。
func TestLiveMetricsReportAppendsPerScenarioAndTotalDuration(t *testing.T) {
	results := []Result{
		{Name: "s1", Passed: true, Turns: 1,
			Usage: llm.Usage{PromptTokens: 3, CompletionTokens: 1}, Duration: 1500 * time.Millisecond},
		{Name: "s2", Passed: false, Turns: 2,
			Usage: llm.Usage{PromptTokens: 2, CompletionTokens: 4, CachedTokens: 1}, Duration: 500 * time.Millisecond},
	}
	got := liveMetricsReport(results)
	want := "# completion rate: 0.500\n" +
		"# tokens: prompt=5 completion=5 cached=1 cache_write=0 total=10\n" +
		"# turns: 3\n" +
		"# scenario 1: tokens prompt=3 completion=1 cached=0 cache_write=0 total=4 duration=1.5s\n" +
		"# scenario 2: tokens prompt=2 completion=4 cached=1 cache_write=0 total=6 duration=500ms\n" +
		"# total duration: 2s\n"
	if got != want {
		t.Fatalf("liveMetricsReport drifted:\n got: %q\nwant: %q", got, want)
	}
}

func TestRunLiveKeepsCJKNamesAndNoKeysInMetricLines(t *testing.T) {
	const fakeKey = "sk-secret-abcdef0123456789"
	scenarios := []Scenario{
		{
			Name:     "中文场景 " + fakeKey,
			Prompt:   "回答",
			Provider: []ScriptTurn{{Respond: Respond{Text: "好的", Usage: &llm.Usage{PromptTokens: 4, CompletionTokens: 1}}}},
			Expect:   Expect{TextContains: []string{"好的"}, Turns: intPtr(1)},
		},
	}
	var out bytes.Buffer
	if _, err := Run(context.Background(), scenarios, fakeRun, Options{Out: &out, Live: true}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	text := out.String()
	if !utf8.ValidString(text) {
		t.Fatalf("output is not valid UTF-8: %q", text)
	}
	if !strings.Contains(text, "ok 1 - 中文场景 "+fakeKey) {
		t.Fatalf("CJK scenario name was not preserved:\n%s", text)
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if len(lines) < 3 {
		t.Fatalf("output has too few lines: %q", text)
	}
	for _, line := range lines[len(lines)-3:] {
		if !strings.HasPrefix(line, "# ") {
			t.Fatalf("unexpected trailing line: %q", line)
		}
		if strings.Contains(line, fakeKey) {
			t.Fatalf("metric line leaked a key-like token: %q", line)
		}
	}
}
