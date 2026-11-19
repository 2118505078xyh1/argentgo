package gui

import (
	"encoding/json"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func writeSyntheticSession(t *testing.T, dir, name string, lines []string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data := []byte(joinLines(lines))
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func joinLines(lines []string) string {
	out := ""
	for _, line := range lines {
		out += line + "\n"
	}
	return out
}

func TestFileUsageFoldsTwoSessions(t *testing.T) {
	dir := t.TempDir()
	sessions := filepath.Join(dir, "sessions")
	writeSyntheticSession(t, sessions, "a.jsonl", []string{
		`{"id":"1","ts":"2026-11-18T10:00:00Z","kind":"header","data":{"title":"a"}}`,
		`{"id":"2","parent":"1","ts":"2026-11-18T10:01:00Z","kind":"user","data":{"text":"hi"}}`,
		`{"id":"3","parent":"2","ts":"2026-11-18T10:02:00Z","kind":"assistant","data":{"text":"hello"}}`,
		`{"id":"4","parent":"3","ts":"2026-11-18T10:03:00Z","kind":"tool","data":{"name":"read","output":"ok"}}`,
		`{"id":"5","parent":"4","ts":"2026-11-18T10:04:00Z","kind":"turn","data":{"model":"m1","requests":2,"warmRequests":1,"promptTokens":100,"completionTokens":20,"cachedTokens":40,"cost":0.01}}`,
	})
	writeSyntheticSession(t, sessions, "b.jsonl", []string{
		`{"id":"1","ts":"2026-11-19T11:00:00Z","kind":"user","data":{"text":"yo"}}`,
		`{"id":"2","parent":"1","ts":"2026-11-19T11:01:00Z","kind":"turn_end","data":{"model":"m2","requests":1,"promptTokens":200,"completionTokens":50,"cachedTokens":20,"cost":0.02}}`,
		`{"id":"3","parent":"2","ts":"2026-11-19T11:02:00Z","kind":"turn","data":{"model":"m1","requests":1,"promptTokens":50,"completionTokens":10,"cachedTokens":10,"cost":0.005}}`,
	})

	summary, err := NewFileUsage(dir).Summary()
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if summary.Turns != 3 || summary.Requests != 4 || summary.WarmRequests != 1 {
		t.Fatalf("turns/requests = %+v", summary)
	}
	if summary.PromptTokens != 350 || summary.CompletionTokens != 80 || summary.CachedTokens != 70 ||
		summary.MissedTokens != 280 || summary.CacheHitPercent != 20 {
		t.Fatalf("tokens = %+v", summary)
	}
	if math.Abs(summary.Cost-0.035) > 1e-9 {
		t.Fatalf("cost = %v, want 0.035", summary.Cost)
	}
	if len(summary.Models) != 2 || summary.Models[0].Name != "m1" ||
		summary.Models[0].Turns != 2 || summary.Models[0].PromptTokens != 150 ||
		summary.Models[1].Name != "m2" {
		t.Fatalf("models = %+v", summary.Models)
	}
	if len(summary.Daily) != 2 || summary.Daily[0].Day != "2026-11-18" ||
		summary.Daily[0].Turns != 1 || summary.Daily[0].Tokens != 120 ||
		summary.Daily[1].Day != "2026-11-19" || summary.Daily[1].Turns != 2 ||
		summary.Daily[1].Tokens != 310 {
		t.Fatalf("daily = %+v", summary.Daily)
	}
}

func TestUsageCommandUsesTheStorageDirDefault(t *testing.T) {
	dir := t.TempDir()
	writeSyntheticSession(t, filepath.Join(dir, "sessions"), "s.jsonl", []string{
		`{"id":"1","ts":"2026-11-19T08:00:00Z","kind":"user","data":{"text":"one"}}`,
		`{"id":"2","ts":"2026-11-19T08:01:00Z","kind":"turn","data":{"model":"m","promptTokens":10,"completionTokens":5,"cost":0.001}}`,
	})
	env := newTestEnv(t, Options{StorageDir: dir})

	status, data := env.rpc("usage_summary", "")
	if status != http.StatusOK {
		t.Fatalf("usage_summary = %d %s", status, data)
	}
	var summary UsageSummary
	if err := json.Unmarshal(data, &summary); err != nil {
		t.Fatalf("decode usage: %v", err)
	}
	if summary.Turns != 1 || summary.PromptTokens != 10 || summary.CompletionTokens != 5 {
		t.Fatalf("usage_summary = %+v", summary)
	}
}
