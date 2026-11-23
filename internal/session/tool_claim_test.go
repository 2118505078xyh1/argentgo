package session

import (
	"encoding/json"
	"testing"
)

// TestToolClaimReplaySkipped 验证非幂等工具调用领取账本：执行前落盘，重放时跳过
// 并把原结果回灌；损坏记录 fail-open（记数不阻断）。
func TestToolClaimReplaySkipped(t *testing.T) {
	s := newTestSession(t, t.TempDir(), "tool-claim")
	if _, err := s.AppendRecord(KindUser, "run the tool"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendRecord(KindAssistant, `{"content":"","tool_calls":[{"id":"call-1","function":{"name":"bash","arguments":"{}"}}]}`); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendToolClaim("call-1"); err != nil {
		t.Fatalf("AppendToolClaim: %v", err)
	}
	if _, err := s.AppendRecord(KindTool, json.RawMessage(`{"tool_call_id":"call-1","name":"bash","output":"done-once"}`)); err != nil {
		t.Fatal(err)
	}

	state, err := s.ToolClaims()
	if err != nil {
		t.Fatalf("ToolClaims: %v", err)
	}
	if !ShouldSkipToolCall(state, "call-1") {
		t.Fatal("claimed tool call was not marked for skip")
	}
	if ShouldSkipToolCall(state, "call-2") {
		t.Fatal("unclaimed tool call was marked for skip")
	}
	result, ok := ResultForClaim(state, "call-1")
	if !ok || result != "done-once" {
		t.Fatalf("replayed result = %q, %v; want done-once, true", result, ok)
	}

	// 损坏的领取记录：计数但不让账本失效，也不阻断其他调用。
	if _, err := s.AppendRecord(KindToolClaim, `{"tool_call_id":`); err != nil {
		t.Fatal(err)
	}
	state, err = s.ToolClaims()
	if err != nil {
		t.Fatalf("ToolClaims after corruption: %v", err)
	}
	if state.Malformed != 1 {
		t.Fatalf("Malformed = %d, want 1", state.Malformed)
	}
	if !ShouldSkipToolCall(state, "call-1") {
		t.Fatal("corrupted record invalidated the whole ledger")
	}
	// 领取记录不进入模型可见消息：追加一条领取后消息数不变。
	before := len(s.Messages())
	if err := s.AppendToolClaim("call-2"); err != nil {
		t.Fatal(err)
	}
	if after := len(s.Messages()); after != before {
		t.Fatalf("tool claim changed model messages: %d -> %d", before, after)
	}
}
