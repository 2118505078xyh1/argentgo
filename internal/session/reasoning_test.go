package session

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// TestAssistantReasoningRoundTrip verifies that an assistant record written
// with reasoning survives a close/reopen and is projected onto Entry, including
// multi-byte CJK text that must not be cut anywhere along the way.
func TestAssistantReasoningRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := newTestSession(t, dir, "reasoning")

	const (
		content   = "答案已给出"
		reasoning = "让我逐步思考：先看输入，再核对边界条件，最后给出答案。"
	)
	if err := s.Append(KindUser, "问题"); err != nil {
		t.Fatalf("Append(user): %v", err)
	}
	if err := s.Append(KindAssistant, AssistantRecord(content, reasoning)); err != nil {
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
	if assistant.Content != content {
		t.Fatalf("content = %q, want %q", assistant.Content, content)
	}
	if assistant.Reasoning != reasoning {
		t.Fatalf("reasoning = %q, want %q", assistant.Reasoning, reasoning)
	}
}

// TestReasoningOfAcceptsWireAliases checks both the neutral reasoning key and
// the OpenAI-compatible reasoning_content key, and that a missing key leaves
// the entry empty so legacy transcripts are unchanged.
func TestReasoningOfAcceptsWireAliases(t *testing.T) {
	cases := []struct {
		name string
		data string
		want string
	}{
		{"neutral key", `{"content":"x","reasoning":"因为所以"}`, "因为所以"},
		{"wire alias", `{"content":"x","reasoning_content":"step by step"}`, "step by step"},
		{"legacy no key", `{"content":"x"}`, ""},
		{"bare string legacy", `"just text"`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := reasoningOf(json.RawMessage(tc.data))
			if got != tc.want {
				t.Fatalf("reasoningOf(%s) = %q, want %q", tc.data, got, tc.want)
			}
		})
	}
}

// TestLegacyAssistantEntryHasNoReasoning guards the compatibility contract: an
// old assistant record without any reasoning key still yields a valid entry
// whose Reasoning is empty.
func TestLegacyAssistantEntryHasNoReasoning(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.jsonl")
	writeRawSession(t, path, []Record{
		{ID: "n000001", Kind: KindUser, Data: json.RawMessage(`{"content":"hi"}`)},
		{ID: "n000002", Parent: "n000001", Kind: KindAssistant, Data: json.RawMessage(`{"content":"hello"}`)},
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
	if len(entries) != 2 || entries[1].Reasoning != "" {
		t.Fatalf("legacy assistant entry = %+v, want empty reasoning", entries)
	}
}

// TestAssistantRecordOmitsEmptyReasoning ensures a write with no reasoning does
// not introduce a new field into the persisted payload, keeping old bytes.
func TestAssistantRecordOmitsEmptyReasoning(t *testing.T) {
	payload := AssistantRecord("done", "")
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "reasoning") {
		t.Fatalf("payload = %s, want no reasoning key", raw)
	}
	withReasoning, err := json.Marshal(AssistantRecord("done", "think"))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(withReasoning), `"reasoning":"think"`) {
		t.Fatalf("payload = %s, want reasoning key", withReasoning)
	}
}

// TestEntryJSONReasoningOmittedWhenEmpty checks the Entry archive encoding:
// empty reasoning is omitted entirely so archives from older builds stay
// byte-identical, and a non-empty value round-trips.
func TestEntryJSONReasoningOmittedWhenEmpty(t *testing.T) {
	empty, err := json.Marshal(Entry{Role: "assistant", Content: "hi"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(empty), "Reasoning") {
		t.Fatalf("entry json = %s, want no Reasoning field", empty)
	}
	in := Entry{Role: "assistant", Content: "hi", Reasoning: "思考"}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Entry
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Reasoning != in.Reasoning {
		t.Fatalf("round-trip reasoning = %q, want %q", out.Reasoning, in.Reasoning)
	}
}
