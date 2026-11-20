package app

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/session"
)

func newReportSession(t *testing.T, title string) *session.Session {
	t.Helper()
	store, err := session.Open(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.NewSession(title)
	if err != nil {
		t.Fatal(err)
	}
	return sess
}

// 累积读取器必须对所有持久化的统计增量求和，而不只是返回
// 最新的那一条，使恢复的会话报告累积总量。
func TestCumulativeSessionUsageAggregatesAcrossRuns(t *testing.T) {
	sess := newReportSession(t, "aggregate")
	if err := sess.AppendStats(session.SessionStats{
		PromptTokens: 100, CompletionTokens: 20, CachedTokens: 10,
		Calls: 1, CostUSD: 0.25, CostSource: "estimated",
	}); err != nil {
		t.Fatal(err)
	}
	if err := sess.AppendStats(session.SessionStats{
		PromptTokens: 250, CompletionTokens: 40, CachedTokens: 30,
		Calls: 2, CostUSD: 0.5, CostSource: "estimated",
	}); err != nil {
		t.Fatal(err)
	}
	if err := sess.AppendUsage(session.UsageRecord{Model: "gpt-x", Input: 100, Output: 20, Cached: 10}); err != nil {
		t.Fatal(err)
	}
	if err := sess.AppendUsage(session.UsageRecord{Model: "gpt-x", Input: 250, Output: 40, Cached: 30}); err != nil {
		t.Fatal(err)
	}

	total, models := cumulativeSessionUsage(sess)
	want := session.SessionStats{
		PromptTokens: 350, CompletionTokens: 60, CachedTokens: 40,
		Calls: 3, CostUSD: 0.75, CostSource: "estimated",
	}
	if total != want {
		t.Fatalf("aggregate totals = %+v, want %+v", total, want)
	}
	if len(models) != 1 {
		t.Fatalf("model breakdown = %+v, want 1 entry", models)
	}
	wantModel := modelUsage{Model: "gpt-x", Input: 350, Output: 60, Cached: 40, Calls: 2}
	if models[0] != wantModel {
		t.Fatalf("model breakdown = %+v, want %+v", models[0], wantModel)
	}
}

// 没有统计或用量记录的会话是正常的空状态：总量为零、没有模型。
// 对尚未打开会话的调用方，nil 会话必须是安全的。
func TestCumulativeSessionUsageEmptySessionIsZero(t *testing.T) {
	sess := newReportSession(t, "empty")

	total, models := cumulativeSessionUsage(sess)
	if total != (session.SessionStats{}) {
		t.Fatalf("empty totals = %+v, want zero", total)
	}
	if len(models) != 0 {
		t.Fatalf("empty model breakdown = %+v, want none", models)
	}

	nilTotal, nilModels := cumulativeSessionUsage(nil)
	if nilTotal != (session.SessionStats{}) || len(nilModels) != 0 {
		t.Fatalf("nil session = (%+v, %+v), want zero/empty", nilTotal, nilModels)
	}
}

// 模型名直接来自用量记录并被原样保留，包括 CJK 字符：
// 读取器不得按字节切片或丢弃非 ASCII rune。
func TestCumulativeSessionUsagePreservesCJKModelNames(t *testing.T) {
	sess := newReportSession(t, "cjk")
	records := []session.UsageRecord{
		{Model: "通义千问-Max", Input: 120, Output: 30, Cached: 5},
		{Model: "通义千问-Max", Input: 80, Output: 10, Cached: 2},
		{Model: "深度求索 R1", Input: 50, Output: 20},
	}
	for _, rec := range records {
		if err := sess.AppendUsage(rec); err != nil {
			t.Fatal(err)
		}
	}

	_, models := cumulativeSessionUsage(sess)
	byName := make(map[string]modelUsage, len(models))
	for _, m := range models {
		byName[m.Model] = m
	}
	if len(byName) != 2 {
		t.Fatalf("model breakdown = %+v, want 2 distinct models", models)
	}
	wantQwen := modelUsage{Model: "通义千问-Max", Input: 200, Output: 40, Cached: 7, Calls: 2}
	if got := byName["通义千问-Max"]; got != wantQwen {
		t.Fatalf("CJK model breakdown = %+v, want %+v", got, wantQwen)
	}
	wantDeepSeek := modelUsage{Model: "深度求索 R1", Input: 50, Output: 20, Calls: 1}
	if got := byName["深度求索 R1"]; got != wantDeepSeek {
		t.Fatalf("CJK model breakdown = %+v, want %+v", got, wantDeepSeek)
	}
}

// 只有 --session 指向某个会话时，--tokens 才会增加累积块；
// 普通报告保持其原有输出，事件格式也不变。
func TestTokenReportWithSessionShowsCumulativeUsage(t *testing.T) {
	root := t.TempDir()
	storage := t.TempDir()
	store, err := session.Open(filepath.Join(storage, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.NewSession("report")
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.AppendStats(session.SessionStats{
		PromptTokens: 900, CompletionTokens: 100, Calls: 4,
		CostUSD: 0.5, CostSource: "estimated",
	}); err != nil {
		t.Fatal(err)
	}
	if err := sess.AppendUsage(session.UsageRecord{Model: "通义千问-Max", Input: 900, Output: 100}); err != nil {
		t.Fatal(err)
	}
	env := envFrom(map[string]string{
		"HOME":           t.TempDir(),
		"USERPROFILE":    t.TempDir(),
		"ARGENT_STORAGE": storage,
	})

	plain, err := TokenReport(cli.IO{Env: env}, cli.Options{Cwd: root})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plain, "cumulative") {
		t.Fatalf("plain --tokens output changed:\n%s", plain)
	}

	addressed, err := TokenReport(cli.IO{Env: env}, cli.Options{Cwd: root, Session: sess.ID()})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"cumulative", "prompt=900", "completion=100", "calls=4",
		"cost=$0.5000", "model[通义千问-Max]",
	} {
		if !strings.Contains(addressed, want) {
			t.Errorf("--tokens --session output missing %q:\n%s", want, addressed)
		}
	}
}
