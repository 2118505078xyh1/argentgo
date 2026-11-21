package session

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// TestAssistantSignatureRoundTrip 验证带签名的 assistant 记录在关闭/重新
// 打开后能存活并投影到 Entry，包括不能被切断的多字节签名文本。
func TestAssistantSignatureRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := newTestSession(t, dir, "reasoning-signature")

	const (
		content   = "答案已给出"
		reasoning = "让我逐步思考：先看输入，再核对边界条件。"
		signature = "签名-αβγ"
	)
	if err := s.Append(KindUser, "问题"); err != nil {
		t.Fatalf("Append(user): %v", err)
	}
	if err := s.Append(KindAssistant, AssistantRecordWithSignature(content, reasoning, signature)); err != nil {
		t.Fatalf("Append(assistant): %v", err)
	}

	reopened, err := OpenSession(dir, s.ID())
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	entries := reopened.Messages()
	if len(entries) != 2 {
		t.Fatalf("Messages() = %d entries, want 2", len(entries))
	}
	assistant := entries[1]
	if assistant.Role != "assistant" {
		t.Fatalf("role = %q, want assistant", assistant.Role)
	}
	if assistant.Reasoning != reasoning {
		t.Fatalf("reasoning = %q, want %q", assistant.Reasoning, reasoning)
	}
	if assistant.ReasoningSignature != signature {
		t.Fatalf("signature = %q, want %q", assistant.ReasoningSignature, signature)
	}
}

// TestAssistantWireSignatureRoundTrip 模拟 agent 循环把 llm.Message 直接
// 作为载荷写入的路径：其签名键为 "reasoning_signature"，读取侧必须能投影。
func TestAssistantWireSignatureRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := newTestSession(t, dir, "wire-signature")
	if err := s.Append(KindAssistant, map[string]any{
		"role":                "assistant",
		"content":             "答案",
		"reasoning":           "思考",
		"reasoning_signature": "sig-wire",
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	reopened, err := OpenSession(dir, s.ID())
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	entries := reopened.Messages()
	if len(entries) != 1 || entries[0].ReasoningSignature != "sig-wire" {
		t.Fatalf("entries = %+v, want signature sig-wire", entries)
	}
}

// TestSignatureOfAcceptsWireAndArchiveKeys 检查中性键与归档键都能读取，
// 缺失键则保持为空，使旧转录不变。
func TestSignatureOfAcceptsWireAndArchiveKeys(t *testing.T) {
	cases := []struct {
		name string
		data string
		want string
	}{
		{"wire key", `{"content":"x","reasoning_signature":"s1"}`, "s1"},
		{"archive key", `{"content":"x","ReasoningSignature":"s2"}`, "s2"},
		{"legacy no key", `{"content":"x"}`, ""},
		{"bare string legacy", `"just text"`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := signatureOf(json.RawMessage(tc.data)); got != tc.want {
				t.Fatalf("signatureOf(%s) = %q, want %q", tc.data, got, tc.want)
			}
		})
	}
}

// TestLegacyAssistantEntryHasNoSignature 守护兼容性契约：没有签名字段的
// 旧 assistant 记录仍能产生有效条目，其 ReasoningSignature 为空。
func TestLegacyAssistantEntryHasNoSignature(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.jsonl")
	writeRawSession(t, path, []Record{
		{ID: "n000001", Kind: KindUser, Data: json.RawMessage(`{"content":"hi"}`)},
		{ID: "n000002", Parent: "n000001", Kind: KindAssistant, Data: json.RawMessage(`{"content":"hello","reasoning":"think"}`)},
	})
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	s, err := store.OpenSession("legacy")
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	entries := s.Messages()
	if len(entries) != 2 || entries[1].ReasoningSignature != "" {
		t.Fatalf("legacy assistant entry = %+v, want empty signature", entries)
	}
}

// TestAssistantRecordWithSignatureOmitsEmpty 确保不带签名的写入不会在
// 持久化载荷中引入新字段，从而保持旧字节；非空签名则正确写入。
func TestAssistantRecordWithSignatureOmitsEmpty(t *testing.T) {
	raw, err := json.Marshal(AssistantRecordWithSignature("done", "think", ""))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "signature") {
		t.Fatalf("payload = %s, want no signature key", raw)
	}
	withSig, err := json.Marshal(AssistantRecordWithSignature("done", "think", "s"))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(withSig), `"reasoning_signature":"s"`) {
		t.Fatalf("payload = %s, want reasoning_signature key", withSig)
	}
}

// TestEntryJSONSignatureOmittedWhenEmpty 检查 Entry 归档编码：空签名被
// 完全省略，使较早版本生成的归档保持逐字节一致；非空值能正确往返。
func TestEntryJSONSignatureOmittedWhenEmpty(t *testing.T) {
	empty, err := json.Marshal(Entry{Role: "assistant", Content: "hi"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(empty), "ReasoningSignature") {
		t.Fatalf("entry json = %s, want no ReasoningSignature field", empty)
	}
	in := Entry{Role: "assistant", Content: "hi", Reasoning: "思考", ReasoningSignature: "sig"}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Entry
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.ReasoningSignature != in.ReasoningSignature {
		t.Fatalf("round-trip signature = %q, want %q", out.ReasoningSignature, in.ReasoningSignature)
	}
}
