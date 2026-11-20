package session

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// 下面的断言刻意检查 Entry 的公开形态，而不是
// 导入器的私有辅助函数，因此它们描述导入契约，而不
// 依赖 Data 载荷在内部如何组织。

func importDataMap(t *testing.T, entry Entry) map[string]any {
	t.Helper()
	var data map[string]any
	if err := json.Unmarshal(entry.Data, &data); err != nil {
		t.Fatalf("unmarshal Entry.Data for %q: %v", entry.Role, err)
	}
	return data
}

func importCallList(t *testing.T, entry Entry) []map[string]any {
	t.Helper()
	raw, ok := importDataMap(t, entry)["tool_calls"]
	if !ok {
		return nil
	}
	values, ok := raw.([]any)
	if !ok {
		t.Fatalf("tool_calls = %T, want array", raw)
	}
	calls := make([]map[string]any, 0, len(values))
	for _, value := range values {
		call, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("tool call = %T, want object", value)
		}
		calls = append(calls, call)
	}
	return calls
}

func importCallID(call map[string]any) string {
	id, _ := call["id"].(string)
	return id
}

func importCallField(call map[string]any, key string) any {
	if value, ok := call[key]; ok {
		return value
	}
	if fn, ok := call["function"].(map[string]any); ok {
		return fn[key]
	}
	return nil
}

func importCallName(call map[string]any) string {
	name, _ := importCallField(call, "name").(string)
	return name
}

func importCallArgumentText(call map[string]any) string {
	switch value := importCallField(call, "arguments").(type) {
	case string:
		return value
	case map[string]any:
		encoded, _ := json.Marshal(value)
		return string(encoded)
	}
	return ""
}

func importUsageValue(t *testing.T, entry Entry, key string) int {
	t.Helper()
	usage, ok := importDataMap(t, entry)["usage"].(map[string]any)
	if !ok {
		t.Fatalf("entry %q has no usage object: %s", entry.Role, entry.Data)
	}
	value, ok := usage[key].(float64)
	if !ok {
		t.Fatalf("usage[%q] = %T, want number", key, usage[key])
	}
	return int(value)
}

func TestImportClaudeCodeJSONLTextAndTimestamps(t *testing.T) {
	const jsonl = `{"type":"user","uuid":"u1","timestamp":"2026-01-01T00:00:00.000Z","cwd":"/tmp","message":{"role":"user","content":"hello"}}
{"type":"assistant","uuid":"u2","timestamp":"2026-01-01T00:00:01.500Z","message":{"role":"assistant","content":[{"type":"text","text":"你好，世界"},{"type":"text","text":"second"}]}}`
	entries, err := ImportClaudeCodeJSONL(strings.NewReader(jsonl))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2: %#v", len(entries), entries)
	}
	if entries[0].Role != "user" || entries[0].Content != "hello" {
		t.Errorf("user entry = %#v", entries[0])
	}
	if entries[1].Role != "assistant" || entries[1].Content != "你好，世界\nsecond" {
		t.Errorf("assistant entry = %#v", entries[1])
	}
	wantFirst := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	wantSecond := time.Date(2026, 1, 1, 0, 0, 1, 500_000_000, time.UTC)
	if !entries[0].TS.Equal(wantFirst) || !entries[1].TS.Equal(wantSecond) {
		t.Errorf("timestamps = %v, %v; want %v, %v", entries[0].TS, entries[1].TS, wantFirst, wantSecond)
	}
	if entries[0].TS.Location() != time.UTC || entries[1].TS.Location() != time.UTC {
		t.Error("timestamps are not normalized to UTC")
	}
	if entries[1].Parent != entries[0].ID {
		t.Errorf("parent chain = %q, want %q", entries[1].Parent, entries[0].ID)
	}
	if text := textOf(entries[1].Data); text != entries[1].Content {
		t.Errorf("Data text = %q, want %q", text, entries[1].Content)
	}
}

func TestImportClaudeCodeJSONLToolUseAndResultPairing(t *testing.T) {
	const jsonl = `{"type":"user","uuid":"u1","timestamp":"2026-01-01T00:00:00Z","message":{"role":"user","content":"run it"}}
{"type":"assistant","uuid":"u2","timestamp":"2026-01-01T00:00:01Z","message":{"role":"assistant","content":[{"type":"text","text":"running"},{"type":"tool_use","id":"toolu_1","name":"bash","input":{"command":"ls"}},{"type":"tool_use","id":"toolu_2","name":"read","input":{"path":"a.txt"}}]}}
{"type":"user","uuid":"u3","timestamp":"2026-01-01T00:00:02Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"text","text":"file.txt"}]},{"type":"tool_result","tool_use_id":"toolu_2","content":"body","is_error":true}]}}`
	entries, err := ImportClaudeCodeJSONL(strings.NewReader(jsonl))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(entries) != 4 {
		t.Fatalf("entries = %d, want 4: %#v", len(entries), entries)
	}
	assistant := entries[1]
	if assistant.Role != "assistant" || assistant.Content != "running" {
		t.Errorf("assistant = %#v", assistant)
	}
	calls := importCallList(t, assistant)
	if len(calls) != 2 || importCallID(calls[0]) != "toolu_1" || importCallID(calls[1]) != "toolu_2" {
		t.Fatalf("tool_calls = %#v", calls)
	}
	if got := importCallName(calls[0]); got != "bash" {
		t.Errorf("first tool name = %q, want bash", got)
	}
	if got := importCallArgumentText(calls[0]); !strings.Contains(got, "ls") {
		t.Errorf("first tool arguments = %q, want the command", got)
	}
	if ids := toolCallIDs(assistant); len(ids) != 2 || ids[0] != "toolu_1" || ids[1] != "toolu_2" {
		t.Errorf("toolCallIDs = %v", ids)
	}

	first, second := entries[2], entries[3]
	if first.Role != "tool" || first.ToolCallID != "toolu_1" || first.Content != "file.txt" {
		t.Errorf("first result = %#v", first)
	}
	if second.Role != "tool" || second.ToolCallID != "toolu_2" || !strings.Contains(second.Content, "body") {
		t.Errorf("second result = %#v", second)
	}
	if !strings.Contains(second.Content, "error") {
		t.Errorf("failed result lost its error marker: %q", second.Content)
	}
	if first.ID == second.ID || first.Parent != entries[1].ID || second.Parent != first.ID {
		t.Errorf("result ids/links = %q/%q, %q/%q", first.ID, second.ID, first.Parent, second.Parent)
	}
	for _, entry := range ReconcileToolPairs(entries) {
		if entry.Content == abortedResultText {
			t.Fatalf("paired call got a synthetic aborted result: %#v", entry)
		}
	}
}

func TestImportClaudeCodeJSONLPreservesUsage(t *testing.T) {
	const jsonl = `{"type":"assistant","uuid":"u2","timestamp":"2026-01-01T00:00:01Z","message":{"role":"assistant","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":7,"output_tokens":50,"cache_creation_input_tokens":1000,"cache_read_input_tokens":5000}}}`
	entries, err := ImportClaudeCodeJSONL(strings.NewReader(jsonl))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	for key, want := range map[string]int{
		"input_tokens":                7,
		"output_tokens":               50,
		"cache_creation_input_tokens": 1000,
		"cache_read_input_tokens":     5000,
	} {
		if got := importUsageValue(t, entries[0], key); got != want {
			t.Errorf("usage[%q] = %d, want %d", key, got, want)
		}
	}
}

func TestImportClaudeCodeJSONLSkipsInvalidAndUnknownLines(t *testing.T) {
	const jsonl = `not json at all
{"type":"attachment","uuid":"u0","timestamp":"2026-01-01T00:00:00Z"}
{"type":"queue-operation","timestamp":"2026-01-01T00:00:00Z"}

[1,2,3]
"a bare string"
42
{"type":"user","uuid":"u1","timestamp":"2026-01-01T00:00:01Z","message":{"role":"user","content":"kept"}}
{"broken":`
	entries, err := ImportClaudeCodeJSONL(strings.NewReader(jsonl))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(entries) != 1 || entries[0].Content != "kept" {
		t.Fatalf("entries = %#v, want only the valid user line", entries)
	}
}

func TestImportClaudeCodeJSONLEmptyInputs(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
	}{
		{name: "empty", src: ""},
		{name: "whitespace", src: "  \n\t\n"},
		{name: "all invalid", src: "garbage\n{} \nnull\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entries, err := ImportClaudeCodeJSONL(strings.NewReader(tc.src))
			if err != nil {
				t.Fatalf("import: %v", err)
			}
			if len(entries) != 0 {
				t.Fatalf("entries = %#v, want empty", entries)
			}
		})
	}
}

func TestImportJSONLRejectsNilReader(t *testing.T) {
	if _, err := ImportClaudeCodeJSONL(nil); err == nil {
		t.Error("Claude Code import accepted a nil reader")
	}
	if _, err := ImportCodexJSONL(nil); err == nil {
		t.Error("Codex import accepted a nil reader")
	}
}

func TestImportClaudeCodeJSONLMissingTimestampsAreZero(t *testing.T) {
	const jsonl = `{"type":"user","message":{"role":"user","content":"no time"}}
{"type":"assistant","timestamp":"not-a-date","message":{"role":"assistant","content":[{"type":"text","text":"bad time"}]}}`
	entries, err := ImportClaudeCodeJSONL(strings.NewReader(jsonl))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	for i, entry := range entries {
		if !entry.TS.IsZero() {
			t.Errorf("entry %d TS = %v, want zero", i, entry.TS)
		}
		if entry.ID == "" {
			t.Errorf("entry %d has no minted id", i)
		}
	}
	if entries[0].ID == entries[1].ID {
		t.Errorf("minted ids collided: %q", entries[0].ID)
	}
}

func TestImportClaudeCodeJSONLProjectsBackToEntries(t *testing.T) {
	const jsonl = `{"type":"user","uuid":"u1","message":{"role":"user","content":"请读文件"}}
{"type":"assistant","uuid":"u2","message":{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"read","input":{"path":"a.txt"}}]}}
{"type":"user","uuid":"u3","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"内容"}]}}`
	imported, err := ImportClaudeCodeJSONL(strings.NewReader(jsonl))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	records := make([]Record, 0, len(imported))
	for _, entry := range imported {
		records = append(records, Record{
			ID:     entry.ID,
			Parent: entry.Parent,
			TS:     entry.TS,
			Kind:   entry.Role,
			Data:   entry.Data,
		})
	}
	projected := messageEntries(records)
	if len(projected) != 3 {
		t.Fatalf("projected = %#v, want 3 entries", projected)
	}
	if projected[0].Role != "user" || projected[0].Content != "请读文件" {
		t.Errorf("user projection = %#v", projected[0])
	}
	if projected[1].Role != "assistant" || len(importCallList(t, projected[1])) != 1 {
		t.Errorf("assistant projection = %#v", projected[1])
	}
	if projected[2].Role != "tool" || projected[2].ToolCallID != "call_1" || projected[2].Content != "内容" {
		t.Errorf("tool projection = %#v", projected[2])
	}
}

func TestImportCodexJSONLBasics(t *testing.T) {
	const jsonl = `{"timestamp":"2026-01-01T00:00:00Z","type":"session_meta","payload":{"id":"s1","cwd":"/tmp"}}
{"timestamp":"2026-01-01T00:00:01Z","type":"event_msg","payload":{"type":"task_started"}}
{"timestamp":"2026-01-01T00:00:02Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"你好"}]}}
{"timestamp":"2026-01-01T00:00:03Z","type":"response_item","payload":{"type":"function_call","call_id":"call_1","name":"read","arguments":"{\"path\":\"a.txt\"}"}}
{"timestamp":"2026-01-01T00:00:04Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call_1","output":"内容"}}
{"timestamp":"2026-01-01T00:00:05Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}}
{"timestamp":"2026-01-01T00:00:06Z","type":"turn_context","payload":{"model":"gpt-5"}}`
	entries, err := ImportCodexJSONL(strings.NewReader(jsonl))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(entries) != 4 {
		t.Fatalf("entries = %d, want 4: %#v", len(entries), entries)
	}
	wantRoles := []string{"user", "assistant", "tool", "assistant"}
	for i, want := range wantRoles {
		if entries[i].Role != want {
			t.Errorf("entry %d role = %q, want %q", i, entries[i].Role, want)
		}
	}
	if entries[0].Content != "你好" || entries[3].Content != "done" {
		t.Errorf("text entries = %q, %q", entries[0].Content, entries[3].Content)
	}
	calls := importCallList(t, entries[1])
	if len(calls) != 1 || importCallID(calls[0]) != "call_1" || importCallName(calls[0]) != "read" {
		t.Errorf("function_call = %#v", calls)
	}
	if !strings.Contains(importCallArgumentText(calls[0]), "a.txt") {
		t.Errorf("function_call arguments lost: %#v", calls[0])
	}
	if entries[2].ToolCallID != "call_1" || entries[2].Content != "内容" {
		t.Errorf("function_call_output = %#v", entries[2])
	}
	if got := toolCallIDs(entries[1]); len(got) != 1 || got[0] != "call_1" {
		t.Errorf("toolCallIDs = %v", got)
	}
	for i := 1; i < len(entries); i++ {
		if entries[i].Parent != entries[i-1].ID {
			t.Errorf("entry %d parent = %q, want %q", i, entries[i].Parent, entries[i-1].ID)
		}
	}
}

func TestImportCodexJSONLEmpty(t *testing.T) {
	entries, err := ImportCodexJSONL(strings.NewReader("{\"type\":\"session_meta\",\"payload\":{\"id\":\"s1\"}}\nnot json\n"))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries = %#v, want empty", entries)
	}
}
