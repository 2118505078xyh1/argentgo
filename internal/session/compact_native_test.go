package session

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestNativeWindowReplay 锁定 #8 的会话侧：替换窗口仅在同一模型展开，
// 模型不一致/窗口缺失/解码失败时静默回退文本压缩。
func TestNativeWindowReplay(t *testing.T) {
	window := []Entry{
		{ID: "w1", Role: "system", Content: "rules"},
		{ID: "w2", Role: "user", Content: "kept"},
	}
	raw, err := json.Marshal(window)
	if err != nil {
		t.Fatal(err)
	}
	compaction := Compaction{Summary: "text fallback", NativeModel: "model-a", NativeWindow: raw}

	got, ok := nativeWindowReplay("model-a", compaction)
	if !ok || len(got) != 2 || got[1].Content != "kept" {
		t.Fatalf("same-model replay = (%+v, %v)", got, ok)
	}
	if got, ok := nativeWindowReplay("model-b", compaction); ok || got != nil {
		t.Fatalf("other-model replay must fall back: (%+v, %v)", got, ok)
	}
	if got, ok := nativeWindowReplay("model-a", Compaction{NativeModel: "model-a"}); ok || got != nil {
		t.Fatalf("missing window must fall back: (%+v, %v)", got, ok)
	}
	if got, ok := nativeWindowReplay("model-a", Compaction{NativeModel: "model-a", NativeWindow: json.RawMessage(`not-json`)}); ok || got != nil {
		t.Fatalf("bad window must fall back: (%+v, %v)", got, ok)
	}

	// 未使用原生压缩的压缩记录不应携带窗口字段，旧字节保持不变。
	legacy, err := json.Marshal(Compaction{Summary: "old"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(legacy), "nativeWindow") || strings.Contains(string(legacy), "nativeModel") {
		t.Fatalf("legacy compaction gained native fields: %s", legacy)
	}
}

// TestRefusalReplayEntries 锁定 #30 的会话侧：丢弃被拒助手轮、把流错误占位
// 改写为固定「未执行」文本，且保留其他条目与输入不变。
func TestRefusalReplayEntries(t *testing.T) {
	entries := []Entry{
		{ID: "1", Role: "user", Content: "first"},
		{ID: "2", Role: "assistant", Content: "I cannot", Data: json.RawMessage(`{"content":"I cannot","stop_reason":"refusal"}`)},
		{ID: "3", Role: "assistant", Content: `{"__synthetic":true,"source":"assistant_stop_error","error":"policy"}`,
			Data: json.RawMessage(`{"__synthetic":true,"source":"assistant_stop_error","error":"policy"}`)},
		{ID: "4", Role: "assistant", Content: "normal", Data: json.RawMessage(`{"content":"normal","finish_reason":"stop"}`)},
		{ID: "5", Role: "user", Content: "second"},
	}
	got := refusalReplayEntries(entries)
	if len(got) != 4 {
		t.Fatalf("replayed length = %d, want 4 (one refusal dropped): %+v", len(got), got)
	}
	if got[1].Content != providerErrorNotExecutedTextSession {
		t.Fatalf("placeholder = %q, want %q", got[1].Content, providerErrorNotExecutedTextSession)
	}
	if strings.Contains(got[1].Content, "policy") {
		t.Fatalf("refusal reason leaked: %q", got[1].Content)
	}
	if got[2].Content != "normal" || got[3].Content != "second" {
		t.Fatalf("unrelated entries changed: %+v", got)
	}
	// 纯函数不改入参。
	if entries[1].Content != "I cannot" || !strings.Contains(entries[2].Content, "policy") {
		t.Fatalf("input mutated: %+v", entries)
	}
}
