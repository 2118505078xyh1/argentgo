package session

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/eval"
)

func newWireSession(t *testing.T) *Session {
	t.Helper()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	s, err := store.NewSession("wire")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	return s
}

func appendMarkedUser(t *testing.T, s *Session, content string, r Retention) {
	t.Helper()
	data := []byte(`{"content":"` + content + `"}`)
	if r != RetainDefault {
		var err error
		data, err = SetRetention(data, r)
		if err != nil {
			t.Fatalf("SetRetention: %v", err)
		}
	}
	if _, err := s.AppendRecord(KindUser, json.RawMessage(data)); err != nil {
		t.Fatalf("AppendRecord: %v", err)
	}
}

func entryContents(entries []Entry) map[string]bool {
	out := make(map[string]bool, len(entries))
	for _, e := range entries {
		out[e.Content] = true
	}
	return out
}

// TestCompactRestoresFlaggedRetentionAcrossCut verifies the compaction wiring:
// a record marked RetainInThread survives a cut that would otherwise drop it,
// while unmarked dropped records stay dropped.
func TestCompactRestoresFlaggedRetentionAcrossCut(t *testing.T) {
	s := newWireSession(t)
	appendMarkedUser(t, s, "m0", RetainInThread)
	for _, name := range []string{"m1", "m2", "m3"} {
		appendMarkedUser(t, s, name, RetainDefault)
	}
	appendMarkedUser(t, s, "m4", RetainDefault)

	if err := s.Compact(1); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	got := entryContents(s.Messages())
	if !got["m0"] {
		t.Fatalf("flagged record m0 did not survive compaction: %v", got)
	}
	if !got["m4"] {
		t.Fatalf("kept suffix record m4 missing: %v", got)
	}
	for _, dropped := range []string{"m1", "m2", "m3"} {
		if got[dropped] {
			t.Fatalf("unmarked record %q should stay compacted: %v", dropped, got)
		}
	}
}

// TestCompactRestoresFlaggedToolPair verifies tool-pair safety: re-emitting a
// flagged tool result also brings back the assistant call that declared it, so
// compaction never leaves an orphaned result in the survived context.
func TestCompactRestoresFlaggedToolPair(t *testing.T) {
	s := newWireSession(t)
	if _, err := s.AppendRecord(KindAssistant, json.RawMessage(
		`{"content":"","tool_calls":[{"id":"c1"}]}`)); err != nil {
		t.Fatalf("append assistant: %v", err)
	}
	data, err := SetRetention([]byte(`{"output":"ok","tool_call_id":"c1"}`), RetainInThread)
	if err != nil {
		t.Fatalf("SetRetention: %v", err)
	}
	if _, err := s.AppendRecord(KindTool, json.RawMessage(data)); err != nil {
		t.Fatalf("append tool result: %v", err)
	}
	appendMarkedUser(t, s, "m2", RetainDefault)
	appendMarkedUser(t, s, "m3", RetainDefault)
	appendMarkedUser(t, s, "m4", RetainDefault)

	if err := s.Compact(1); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	var sawCall, sawResult bool
	for _, e := range s.Messages() {
		if e.Role == "tool" && e.ToolCallID == "c1" {
			sawResult = true
		}
		if e.Role == "assistant" {
			for _, id := range toolCallIDs(e) {
				if id == "c1" {
					sawCall = true
				}
			}
		}
	}
	if !sawResult || !sawCall {
		t.Fatalf("tool pair not preserved: call=%v result=%v", sawCall, sawResult)
	}
}

// TestCompactBudgetRestoresFlaggedRetention verifies the budget overload also
// honours retention hints, not only Compact(keep).
func TestCompactBudgetRestoresFlaggedRetention(t *testing.T) {
	s := newWireSession(t)
	appendMarkedUser(t, s, "old-flagged", RetainAlways)
	for _, name := range []string{"old-a", "old-b", "old-c"} {
		appendMarkedUser(t, s, name, RetainDefault)
	}
	appendMarkedUser(t, s, "recent", RetainDefault)

	// A small budget keeps only the newest message; the flagged one is older.
	if err := s.CompactBudget(40); err != nil {
		t.Fatalf("CompactBudget: %v", err)
	}
	got := entryContents(s.Messages())
	if !got["old-flagged"] {
		t.Fatalf("flagged record did not survive budget compaction: %v", got)
	}
}

// TestAgentStatsWireShapeReadsBack validates the wire contract between the
// agent's stat payload (JSON field names) and this package's readers.
func TestAgentStatsWireShapeReadsBack(t *testing.T) {
	s := newWireSession(t)
	payload := json.RawMessage(`{
		"prompt_tokens":120,"completion_tokens":30,"cached_tokens":10,
		"calls":2,"cost_usd":0.0123,"cost_source":"estimated",
		"updated":"2026-11-20T00:00:00Z"
	}`)
	if _, err := s.AppendRecord("stats", payload); err != nil {
		t.Fatalf("AppendRecord: %v", err)
	}
	got, ok := s.LatestStats()
	if !ok {
		t.Fatal("LatestStats: no stats record")
	}
	want := SessionStats{
		PromptTokens: 120, CompletionTokens: 30, CachedTokens: 10,
		Calls: 2, CostUSD: 0.0123, CostSource: "estimated",
		Updated: "2026-11-20T00:00:00Z",
	}
	if got != want {
		t.Fatalf("LatestStats = %+v, want %+v", got, want)
	}
	if agg := s.AggregateStats(); agg != want {
		t.Fatalf("AggregateStats = %+v, want %+v", agg, want)
	}
}

// ---------------------------------------------------------------------------
// Exec check: opt-in command execution in the eval harness.
// (Kept in the session test package because eval imports agent, so an agent
// test importing eval would form an import cycle.)
// ---------------------------------------------------------------------------

// TestEvalExecCheckOptIn builds a trivial Go module in the seeded workspace and
// verifies an explicitly enabled Exec check runs and grades successfully.
func TestEvalExecCheckOptIn(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	doc := []byte(`{"scenarios":[{
		"name":"build trivial module",
		"prompt":"noop",
		"files":{
			"go.mod":"module example.com/wiretest\n\ngo 1.21\n",
			"main.go":"package main\nfunc main() {}\n"
		},
		"provider":[],
		"check":{
			"allow_exec":true,
			"exec":{"command":"go","args":["build","./..."],"timeout_ms":30000,"want_exit_code":0}
		}
	}]}`)
	scenarios, err := eval.Parse(doc)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if scenarios[0].Check.Exec == nil || !scenarios[0].Check.AllowExec {
		t.Fatalf("exec check fields not decoded: %+v", scenarios[0].Check)
	}
	var out bytes.Buffer
	report, err := eval.Run(context.Background(), scenarios,
		func(context.Context, eval.Scenario, eval.Workspace) (eval.Trace, error) {
			return eval.Trace{Text: "ok"}, nil
		}, eval.Options{Out: &out})
	if err != nil {
		t.Fatalf("eval run: %v\n%s", err, out.String())
	}
	if report.Passed != 1 {
		t.Fatalf("report: %+v\n%s", report, out.String())
	}
}

// TestEvalExecCheckDefaultOff verifies a command is never run unless the
// scenario opts in: an Exec that would fail is ignored when AllowExec is false.
func TestEvalExecCheckDefaultOff(t *testing.T) {
	doc := []byte(`{"scenarios":[{
		"name":"exec not permitted",
		"prompt":"noop",
		"files":{
			"go.mod":"module example.com/wiretest\n\ngo 1.21\n",
			"main.go":"package main\nfunc main() {}\n"
		},
		"provider":[],
		"check":{
			"exec":{"command":"go","args":["build","./..."],"want_exit_code":99}
		}
	}]}`)
	scenarios, err := eval.Parse(doc)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if scenarios[0].Check.AllowExec {
		t.Fatal("AllowExec must default to false")
	}
	var out bytes.Buffer
	report, err := eval.Run(context.Background(), scenarios,
		func(context.Context, eval.Scenario, eval.Workspace) (eval.Trace, error) {
			return eval.Trace{Text: "ok"}, nil
		}, eval.Options{Out: &out})
	if err != nil {
		t.Fatalf("eval run should pass when exec is disabled: %v\n%s", err, out.String())
	}
	if report.Passed != 1 {
		t.Fatalf("report: %+v\n%s", report, out.String())
	}
}
