package eval

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/llm"
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
	const want = "1..2\n" +
		"ok 1 - pass\n" +
		"not ok 2 - fail: the answer does not contain \"never\" (got \"ok\")\n" +
		"# 1 passed, 1 failed\n" +
		"# completion rate: 0.500\n" +
		"# tokens: prompt=15 completion=5 cached=1 cache_write=0 total=20\n" +
		"# turns: 2\n"
	if got := out.String(); got != want {
		t.Fatalf("live output drifted:\n got: %q\nwant: %q", got, want)
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
