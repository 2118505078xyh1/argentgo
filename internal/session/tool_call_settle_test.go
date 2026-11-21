package session

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

// settledReasonOf 读取合成结果 Data 中携带的原因。
func settledReasonOf(t *testing.T, entry Entry) string {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(entry.Data, &payload); err != nil {
		t.Fatalf("unmarshal settled data: %v", err)
	}
	reason, _ := payload[settleReasonKey].(string)
	return reason
}

// pairCount 统计携带给定调用 id 的 tool 结果条数。
func pairCount(entries []Entry, callID string) int {
	n := 0
	for _, entry := range entries {
		if entry.Role == "tool" && entry.ToolCallID == callID {
			n++
		}
	}
	return n
}

func TestSettleOrphanToolCallsFillsPair(t *testing.T) {
	in := []Entry{
		{Role: "user", Content: "read two files"},
		reconcilerCall("c1", "c2"),
		reconcilerResult("c1", "first result"),
		{Role: "user", Content: "next question"},
	}
	got := SettleOrphanToolCalls(in, SettleInterrupted)

	if len(got) != len(in)+1 {
		t.Fatalf("len = %d, want %d: %+v", len(got), len(in)+1, got)
	}
	// 合成结果紧跟在声明它的 assistant 条目之后，不越过后续用户消息。
	if got[2].Role != "tool" || got[2].ToolCallID != "c2" {
		t.Fatalf("synthetic should follow the assistant call, got %+v", got[2])
	}
	synthetic := got[2]
	if synthetic.Content != abortedResultText {
		t.Errorf("content = %q, want %q", synthetic.Content, abortedResultText)
	}
	if synthetic.ID != settleResultID("c2") {
		t.Errorf("id = %q, want %q", synthetic.ID, settleResultID("c2"))
	}
	if !isSyntheticAborted(synthetic) {
		t.Error("synthetic result must stay recognizable by isSyntheticAborted")
	}
	if reason := settledReasonOf(t, synthetic); reason != string(SettleInterrupted) {
		t.Errorf("reason = %q, want %q", reason, SettleInterrupted)
	}
	// 每个调用恰好配对一次；真实结果保持不变。
	if pairCount(got, "c1") != 1 || pairCount(got, "c2") != 1 {
		t.Fatalf("pairing incomplete: c1=%d c2=%d", pairCount(got, "c1"), pairCount(got, "c2"))
	}
	// 真实结果与后续用户消息都顺延一位，顺序不变。
	if got[3].Role != "tool" || got[3].ToolCallID != "c1" || got[3].Content != "first result" {
		t.Errorf("real result moved: %+v", got[3])
	}
	if got[4].Role != "user" || got[4].Content != "next question" {
		t.Errorf("later user message moved: %+v", got[4])
	}
	// 输入未被修改。
	if len(in) != 4 {
		t.Fatalf("input mutated: %+v", in)
	}
}

func TestSettleOrphanToolCallsCleanByteIdentical(t *testing.T) {
	clean := []Entry{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "question"},
		reconcilerCall("c1"),
		reconcilerResult("c1", "result one"),
		{Role: "assistant", Content: "final answer"},
	}
	got := SettleOrphanToolCalls(clean, SettleInterrupted)
	if !reflect.DeepEqual(got, clean) {
		t.Fatalf("clean session changed:\n got %+v\nwant %+v", got, clean)
	}
	for _, entry := range got {
		if isSyntheticAborted(entry) {
			t.Errorf("clean session gained a synthetic result: %+v", entry)
		}
	}

	noMeta := []Entry{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "thinking"},
		{Role: "tool", Content: "plain tool output"},
	}
	if got := SettleOrphanToolCalls(noMeta, SettleInterrupted); !reflect.DeepEqual(got, noMeta) {
		t.Fatalf("no-metadata entries changed:\n got %+v\nwant %+v", got, noMeta)
	}
	if got := SettleOrphanToolCalls(nil, SettleInterrupted); got != nil {
		t.Errorf("nil input returned %+v, want nil", got)
	}
	if got := SettleOrphanToolCalls([]Entry{}, SettleInterrupted); len(got) != 0 {
		t.Errorf("empty input returned %+v, want empty", got)
	}
}

func TestSettleResultIDDeterministic(t *testing.T) {
	first := settleResultID("call-42")
	if second := settleResultID("call-42"); first != second {
		t.Fatalf("id not deterministic: %q vs %q", first, second)
	}
	if len(first) != 12 {
		t.Errorf("id length = %d, want 12", len(first))
	}
	if _, err := hex.DecodeString(first); err != nil {
		t.Errorf("id is not hex: %v", err)
	}
	if other := settleResultID("call-43"); other == first {
		t.Errorf("different call ids produced the same id %q", first)
	}
}

func TestSettleOrphanToolCallsIdempotent(t *testing.T) {
	in := []Entry{
		{Role: "user", Content: "go"},
		reconcilerCall("dangling"),
	}
	first := SettleOrphanToolCalls(in, SettleInterrupted)
	second := SettleOrphanToolCalls(first, SettleInterrupted)
	third := SettleOrphanToolCalls(second, SettleInterrupted)
	if !reflect.DeepEqual(first, second) || !reflect.DeepEqual(second, third) {
		t.Fatalf("settlement is not idempotent:\nfirst  %+v\nsecond %+v\nthird  %+v", first, second, third)
	}
	if pairCount(first, "dangling") != 1 || pairCount(third, "dangling") != 1 {
		t.Fatalf("idempotency produced duplicate results: %+v", third)
	}
}

func TestSettleOrphanToolCallsCJKAndSpecialIDs(t *testing.T) {
	ids := []string{
		"call-中文-工具",          // CJK 调用 id
		"call-🔥-emoji",        // 多字节 emoji
		`call-"quoted"\slash`, // 引号与反斜杠
		"call-line\nbreak",    // 控制字符
		"",                    // 空 id 会被忽略
	}
	in := []Entry{{Role: "assistant", Content: "many tools"}, reconcilerCall(ids...)}
	got := SettleOrphanToolCalls(in, SettleCanceled)

	for _, id := range ids {
		if id == "" {
			continue
		}
		if pairCount(got, id) != 1 {
			t.Errorf("call %q not paired exactly once", id)
		}
		// 合成 id 必须是合法的十六进制短串，不受调用 id 中的特殊字符影响。
		want := settleResultID(id)
		if len(want) != 12 {
			t.Errorf("settleResultID(%q) = %q, want 12 chars", id, want)
		}
		if _, err := hex.DecodeString(want); err != nil {
			t.Errorf("settleResultID(%q) = %q is not hex: %v", id, want, err)
		}
	}
	if reason := settledReasonOf(t, got[2]); reason != string(SettleCanceled) {
		t.Errorf("reason = %q, want %q", reason, SettleCanceled)
	}
}

func TestSettleOrphanToolCallsEnrichesReconcileResults(t *testing.T) {
	// 先走 ReconcileToolPairs 得到 64 位中止结果，再交给结算层补齐原因与短 id。
	reconciled := ReconcileToolPairs([]Entry{
		{Role: "user", Content: "go"},
		reconcilerCall("legacy-call"),
	})
	if len(reconciled) != 3 || !isSyntheticAborted(reconciled[2]) {
		t.Fatalf("unexpected reconcile output: %+v", reconciled)
	}
	if reconciled[2].ID == settleResultID("legacy-call") {
		t.Fatal("reconciled synthetic id unexpectedly already short")
	}

	settled := SettleOrphanToolCalls(reconciled, SettleInterrupted)
	if len(settled) != len(reconciled) {
		t.Fatalf("enrichment changed length: %d -> %d", len(reconciled), len(settled))
	}
	if settled[2].ID != settleResultID("legacy-call") {
		t.Errorf("id = %q, want %q", settled[2].ID, settleResultID("legacy-call"))
	}
	if settled[2].Content != abortedResultText {
		t.Errorf("content changed during enrichment: %q", settled[2].Content)
	}
	if reason := settledReasonOf(t, settled[2]); reason != string(SettleInterrupted) {
		t.Errorf("reason = %q, want %q", reason, SettleInterrupted)
	}
	// 二次结算仍然幂等。
	if again := SettleOrphanToolCalls(settled, SettleInterrupted); !reflect.DeepEqual(again, settled) {
		t.Fatalf("enriched settlement not idempotent:\n got %+v\nwant %+v", again, settled)
	}
}

func TestSettleOrphanToolCallsCapsSyntheticResults(t *testing.T) {
	ids := make([]string, 0, maxSettledResults+5)
	for i := 0; i < maxSettledResults+5; i++ {
		ids = append(ids, "call-cap-"+string(rune('a'+i%26))+string(rune('a'+i/26)))
	}
	in := []Entry{reconcilerCall(ids...)}
	got := SettleOrphanToolCalls(in, SettleInterrupted)
	if len(got) != 1+maxSettledResults {
		t.Fatalf("len = %d, want %d", len(got), 1+maxSettledResults)
	}
}

func TestSettleOrphanToolCallsTruncatedTurn(t *testing.T) {
	// 截断场景：assistant 发出调用后会话直接结束，没有任何结果。
	in := []Entry{
		{Role: "user", Content: "run a long task"},
		reconcilerCall("truncated-call"),
	}
	got := SettleOrphanToolCalls(in, SettleInterrupted)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3: %+v", len(got), got)
	}
	if got[2].Role != "tool" || got[2].ToolCallID != "truncated-call" {
		t.Fatalf("truncated call not settled: %+v", got[2])
	}
	if reason := settledReasonOf(t, got[2]); reason != string(SettleInterrupted) {
		t.Errorf("reason = %q, want %q", reason, SettleInterrupted)
	}
}

func TestMessagesSettlesOrphanOnLoad(t *testing.T) {
	sess := newTestSession(t, t.TempDir(), "settle-load")
	if err := sess.Append(KindUser, "do work"); err != nil {
		t.Fatalf("Append(user): %v", err)
	}
	callData, _ := json.Marshal(map[string]any{
		"role":       "assistant",
		"content":    "calling a tool",
		"tool_calls": []any{map[string]any{"id": "load-orphan", "type": "function"}},
	})
	if _, err := sess.AppendRecord(KindAssistant, json.RawMessage(callData)); err != nil {
		t.Fatalf("AppendRecord(assistant): %v", err)
	}

	messages := sess.Messages()
	if len(messages) != 3 {
		t.Fatalf("Messages() = %d entries, want 3: %+v", len(messages), messages)
	}
	last := messages[2]
	if last.Role != "tool" || last.ToolCallID != "load-orphan" {
		t.Fatalf("orphan call not paired on load: %+v", last)
	}
	if last.ID != settleResultID("load-orphan") {
		t.Errorf("id = %q, want %q", last.ID, settleResultID("load-orphan"))
	}
	if reason := settledReasonOf(t, last); reason != string(SettleInterrupted) {
		t.Errorf("reason = %q, want %q", reason, SettleInterrupted)
	}

	// 再次读取得到相同结果（幂等），且底层 JSONL 文件字节未被改写。
	again := sess.Messages()
	if !reflect.DeepEqual(messages, again) {
		t.Fatalf("repeated Messages() differs:\nfirst  %+v\nsecond %+v", messages, again)
	}
	raw, err := os.ReadFile(sess.path())
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	for _, line := range []string{"reason", settleResultID("load-orphan"), abortedResultText} {
		if strings.Contains(string(raw), line) {
			t.Errorf("synthetic result was persisted to disk: found %q", line)
		}
	}
}

func TestMessagesCleanSessionFileUnchanged(t *testing.T) {
	sess := newTestSession(t, t.TempDir(), "settle-clean")
	if err := sess.Append(KindUser, "hello"); err != nil {
		t.Fatalf("Append(user): %v", err)
	}
	if err := sess.Append(KindAssistant, "hi"); err != nil {
		t.Fatalf("Append(assistant): %v", err)
	}
	before, err := os.ReadFile(sess.path())
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	messages := sess.Messages()
	if len(messages) != 2 || messages[0].Content != "hello" || messages[1].Content != "hi" {
		t.Fatalf("clean projection changed: %+v", messages)
	}
	after, err := os.ReadFile(sess.path())
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(before) != string(after) {
		t.Fatal("clean session file changed after Messages()")
	}
	for _, entry := range messages {
		if isSyntheticAborted(entry) {
			t.Errorf("clean session gained a synthetic result: %+v", entry)
		}
	}
}
