package session

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"testing"

	"example.com/argentgo/internal/eval"
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

// TestCompactRestoresFlaggedRetentionAcrossCut 验证压缩接线：
// 标记为 RetainInThread 的记录能熬过原本会丢弃它的切分，
// 而未标记的被丢弃记录仍被丢弃。
func TestCompactRestoresFlaggedRetentionAcrossCut(t *testing.T) {
	t.Setenv(summarySchemaEnv, "0")
	t.Setenv(foveatedArchiveEnv, "0")
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

// TestCompactRestoresFlaggedToolPair 验证工具配对安全性：重新发出带标记的
// 工具结果也会带回声明它的 assistant 调用，因此压缩绝不会在保留的
// 上下文中留下孤立的结果。
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

// TestCompactBudgetRestoresFlaggedRetention 验证预算重载也遵守保留提示，
// 而不仅是 Compact(keep)。
func TestCompactBudgetRestoresFlaggedRetention(t *testing.T) {
	s := newWireSession(t)
	appendMarkedUser(t, s, "old-flagged", RetainAlways)
	for _, name := range []string{"old-a", "old-b", "old-c"} {
		appendMarkedUser(t, s, name, RetainDefault)
	}
	appendMarkedUser(t, s, "recent", RetainDefault)

	// 小预算只保留最新的消息；带标记的那条较旧。
	if err := s.CompactBudget(40); err != nil {
		t.Fatalf("CompactBudget: %v", err)
	}
	got := entryContents(s.Messages())
	if !got["old-flagged"] {
		t.Fatalf("flagged record did not survive budget compaction: %v", got)
	}
}

// TestAgentStatsWireShapeReadsBack 验证 agent 统计载荷（JSON 字段名）与本包
// 读取器之间的线格式契约。
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
// Exec 检查：eval 框架中选择启用的命令执行。
// （保留在 session 测试包中，因为 eval 导入 agent，若 agent 测试
// 导入 eval 会形成导入循环。）
// ---------------------------------------------------------------------------

// TestEvalExecCheckOptIn 在种子工作区构建一个简单 Go 模块，并验证显式启用的
// Exec 检查能运行并成功评分。
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

// TestEvalExecCheckDefaultOff 验证除非场景选择启用，否则绝不运行命令：
// 当 AllowExec 为 false 时，一个本会失败的 Exec 会被忽略。
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
