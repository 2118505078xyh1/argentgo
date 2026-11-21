package session

import (
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// reconcilerCall 构建 assistant Entry，其 Data 携带 agent 循环持久化的
// 完整 llm.Message "tool_calls" 载荷形态。
func reconcilerCall(ids ...string) Entry {
	calls := make([]any, 0, len(ids))
	for _, id := range ids {
		calls = append(calls, map[string]any{"id": id, "type": "function"})
	}
	data, _ := json.Marshal(map[string]any{
		"role":       "assistant",
		"content":    "calling tools",
		"tool_calls": calls,
	})
	return Entry{Role: "assistant", Content: "calling tools", Data: data}
}

// reconcilerResult 构建工具 Entry，其结果 id 记录在
// messageEntry 映射为 ToolCallID 的键（"id"）下。
func reconcilerResult(id, output string) Entry {
	data, _ := json.Marshal(map[string]any{"id": id, "name": "read", "output": output})
	return Entry{Role: "tool", Content: output, ToolCallID: id, Data: data}
}

// abortedEntry 是悬空调用 id 所期望的合成结果。
func abortedEntry(callID string) Entry {
	return Entry{
		ID:         abortedResultID(callID),
		Role:       "tool",
		Content:    abortedResultText,
		ToolCallID: callID,
	}
}

func TestReconcileDropsOrphanToolResult(t *testing.T) {
	orphan := reconcilerResult("orphan", "nobody asked for this")
	in := []Entry{
		{Role: "user", Content: "hello"},
		orphan,
		{Role: "assistant", Content: "done"},
	}
	got := ReconcileToolPairs(in)
	want := []Entry{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "done"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("orphan result not dropped:\n got %+v\nwant %+v", got, want)
	}
	// 输入必须不被修改。
	if len(in) != 3 || in[1].ID != orphan.ID {
		t.Fatalf("input slice was mutated: %+v", in)
	}
}

func TestReconcileDanglingCallGetsStableAbortedResult(t *testing.T) {
	in := []Entry{
		{Role: "user", Content: "read a file"},
		reconcilerCall("call-1"),
	}
	first := ReconcileToolPairs(in)
	second := ReconcileToolPairs(in)
	if len(first) != 3 || len(second) != 3 {
		t.Fatalf("lengths = %d and %d, want 3", len(first), len(second))
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("reconciliation is not deterministic:\nfirst  %+v\nsecond %+v", first, second)
	}

	synthetic := first[2]
	if synthetic.Role != "tool" {
		t.Errorf("synthetic role = %q, want tool", synthetic.Role)
	}
	if synthetic.ToolCallID != "call-1" {
		t.Errorf("synthetic ToolCallID = %q, want call-1", synthetic.ToolCallID)
	}
	if synthetic.Content != abortedResultText {
		t.Errorf("synthetic content = %q, want %q", synthetic.Content, abortedResultText)
	}
	wantID := abortedResultID("call-1")
	if synthetic.ID != wantID {
		t.Errorf("synthetic id = %q, want %q", synthetic.ID, wantID)
	}
	if len(synthetic.ID) != 64 {
		t.Errorf("synthetic id length = %d, want 64 hex chars", len(synthetic.ID))
	}
	if _, err := hex.DecodeString(synthetic.ID); err != nil {
		t.Errorf("synthetic id is not hex: %v", err)
	}
}

func TestReconcileMatchedPairsByteIdentical(t *testing.T) {
	clean := []Entry{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "question"},
		reconcilerCall("c1"),
		reconcilerResult("c1", "result one"),
		{Role: "assistant", Content: "final answer"},
	}
	got := ReconcileToolPairs(clean)
	if !reflect.DeepEqual(got, clean) {
		t.Fatalf("clean matched pairs changed:\n got %+v\nwant %+v", got, clean)
	}

	// 没有调用元数据的工具条目必须原样通过。
	noMeta := []Entry{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "thinking"},
		{Role: "tool", Content: "plain tool output"},
	}
	got = ReconcileToolPairs(noMeta)
	if !reflect.DeepEqual(got, noMeta) {
		t.Fatalf("no-metadata entries changed:\n got %+v\nwant %+v", got, noMeta)
	}

	// nil 和空输入保持不变。
	if got := ReconcileToolPairs(nil); got != nil {
		t.Errorf("nil input returned %+v, want nil", got)
	}
	if got := ReconcileToolPairs([]Entry{}); len(got) != 0 {
		t.Errorf("empty input returned %+v, want empty", got)
	}
}

func TestReconcileMixedCase(t *testing.T) {
	orphan := reconcilerResult("orphan", "stray output")
	goodCall := reconcilerCall("good")
	goodResult := reconcilerResult("good", "proper output")
	danglingCall := reconcilerCall("dangling")

	in := []Entry{
		{Role: "user", Content: "start"},
		orphan,
		goodCall,
		goodResult,
		danglingCall,
		{Role: "assistant", Content: "end"},
	}
	got := ReconcileToolPairs(in)

	want := []Entry{
		{Role: "user", Content: "start"},
		goodCall,
		goodResult,
		danglingCall,
		abortedEntry("dangling"),
		{Role: "assistant", Content: "end"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mixed reconciliation mismatch:\n got %+v\nwant %+v", got, want)
	}
}

func TestReconcileCapsSyntheticResults(t *testing.T) {
	ids := make([]string, 0, maxSyntheticResults+5)
	for i := 0; i < maxSyntheticResults+5; i++ {
		ids = append(ids, "call-"+string(rune('a'+i%26))+"-"+string(rune('a'+i/26)))
	}
	call := reconcilerCall(ids...)
	in := []Entry{call}
	before := len(in)

	got := ReconcileToolPairs(in)
	if len(got) != 1+maxSyntheticResults {
		t.Fatalf("got %d entries, want %d (1 call + %d synthetic)",
			len(got), 1+maxSyntheticResults, maxSyntheticResults)
	}
	for i := 1; i < len(got); i++ {
		wantID := ids[i-1]
		if got[i].ToolCallID != wantID {
			t.Errorf("synthetic %d ToolCallID = %q, want %q", i, got[i].ToolCallID, wantID)
		}
		if got[i].ID != abortedResultID(wantID) {
			t.Errorf("synthetic %d id is not deterministic for %q", i, wantID)
		}
	}
	if len(in) != before {
		t.Fatalf("input slice length changed: %d -> %d", before, len(in))
	}
}

func TestMessagesReconcilesDanglingCallAtReplay(t *testing.T) {
	sess := newTestSession(t, t.TempDir(), "replay")
	callData, _ := json.Marshal(map[string]any{
		"role":       "assistant",
		"content":    "calling a tool",
		"tool_calls": []any{map[string]any{"id": "replay-call", "type": "function"}},
	})
	if _, err := sess.AppendRecord(KindAssistant, json.RawMessage(callData)); err != nil {
		t.Fatalf("AppendRecord(assistant): %v", err)
	}

	messages := sess.Messages()
	if len(messages) != 2 {
		t.Fatalf("Messages() = %d entries, want 2 (assistant + synthetic result): %+v",
			len(messages), messages)
	}
	last := messages[1]
	if last.Role != "tool" || last.ToolCallID != "replay-call" || last.Content != abortedResultText {
		t.Fatalf("replay did not synthesize an aborted result: %+v", last)
	}
	// 结算层把 ReconcileToolPairs 的中止结果换成确定性的短 id（sha256[:12]）。
	if last.ID != settleResultID("replay-call") {
		t.Errorf("synthetic id = %q, want %q", last.ID, settleResultID("replay-call"))
	}
	if len(last.ID) != 12 {
		t.Errorf("synthetic id length = %d, want 12", len(last.ID))
	}
}

func TestMessagesProjectionUnchangedForCleanSession(t *testing.T) {
	sess := newTestSession(t, t.TempDir(), "clean")
	if err := sess.Append(KindUser, "read a file"); err != nil {
		t.Fatalf("Append(user): %v", err)
	}
	callData, _ := json.Marshal(map[string]any{
		"role":       "assistant",
		"content":    "calling a tool",
		"tool_calls": []any{map[string]any{"id": "clean-call", "type": "function"}},
	})
	if _, err := sess.AppendRecord(KindAssistant, json.RawMessage(callData)); err != nil {
		t.Fatalf("AppendRecord(assistant): %v", err)
	}
	resultData, _ := json.Marshal(map[string]any{"id": "clean-call", "output": "file contents"})
	if _, err := sess.AppendRecord(KindTool, json.RawMessage(resultData)); err != nil {
		t.Fatalf("AppendRecord(tool): %v", err)
	}
	if err := sess.Append(KindAssistant, "done"); err != nil {
		t.Fatalf("Append(assistant): %v", err)
	}

	messages := sess.Messages()
	if len(messages) != 4 {
		t.Fatalf("Messages() = %d entries, want 4: %+v", len(messages), messages)
	}
	if messages[0].Role != "user" || messages[0].Content != "read a file" {
		t.Errorf("message 0 = %+v", messages[0])
	}
	if messages[1].Role != "assistant" {
		t.Errorf("message 1 role = %q, want assistant", messages[1].Role)
	}
	if messages[2].Role != "tool" || messages[2].ToolCallID != "clean-call" || messages[2].Content != "file contents" {
		t.Errorf("message 2 = %+v", messages[2])
	}
	if messages[3].Role != "assistant" || messages[3].Content != "done" {
		t.Errorf("message 3 = %+v", messages[3])
	}
	for _, message := range messages {
		if strings.Contains(message.Content, abortedResultText) {
			t.Errorf("clean session produced a synthetic result: %+v", message)
		}
	}
}
