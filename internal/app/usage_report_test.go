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

// The cumulative reader must sum every persisted stats delta, not just return
// the newest one, so a resumed session reports the accumulated total.
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

// A session with no stats or usage records is a normal empty state: zero totals
// and no models. A nil session must be safe for callers that have not opened one.
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

// Model names come straight from usage records and are kept verbatim, including
// CJK characters: the reader must not slice bytes or drop non-ASCII runes.
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

// --tokens gains the cumulative block only when --session addresses a session;
// the plain report keeps its exact prior output and no event format changes.
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
