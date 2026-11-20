package agent

import (
	"context"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// TestChatNormalizesToolPairingForProvider checks that chat() repairs an orphan
// tool_use in the message view it hands to Chatter, without mutating the run's
// own transcript (the repair is view-layer only).
func TestChatNormalizesToolPairingForProvider(t *testing.T) {
	chat := &fakeChatter{fn: func(int, []llm.Message, []llm.ToolSpec, func(string)) (llm.Message, error) {
		return textReply("ok"), nil
	}}
	seed := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hi"},
		{Role: "assistant", ToolCalls: []llm.ToolCall{
			toolCall("c1", "read_file", "{}"),
		}},
	}
	s := &runState{
		model:    "test",
		opts:     Options{Chatter: chat},
		messages: seed,
	}

	if _, err := s.chat(context.Background(), nil); err != nil {
		t.Fatalf("chat() error: %v", err)
	}

	seen := chat.requestsSeen()
	if len(seen) != 1 {
		t.Fatalf("provider saw %d requests, want 1", len(seen))
	}
	got := seen[0]

	// A synthetic tool result for the orphaned call must appear in the view.
	found := false
	for _, m := range got {
		if m.Role == "tool" && m.ToolCallID == "c1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("provider messages lack a result for the orphan call c1: %+v", got)
	}
	if len(got) != len(seed)+1 {
		t.Fatalf("provider saw %d messages, want %d (one synthetic result)", len(got), len(seed)+1)
	}
	// The run's transcript must be untouched.
	if len(s.messages) != len(seed) || s.messages[2].Role != "assistant" {
		t.Fatalf("s.messages was mutated by the view repair: %+v", s.messages)
	}
}

// TestChatPassesPairedTranscriptUnchanged checks that a fully paired transcript
// reaches Chatter by reference, with no synthetic messages inserted.
func TestChatPassesPairedTranscriptUnchanged(t *testing.T) {
	chat := &fakeChatter{fn: func(int, []llm.Message, []llm.ToolSpec, func(string)) (llm.Message, error) {
		return textReply("ok"), nil
	}}
	seed := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hi"},
		{Role: "assistant", ToolCalls: []llm.ToolCall{toolCall("c1", "read_file", "{}")}},
		{Role: "tool", ToolCallID: "c1", Content: "file contents"},
	}
	s := &runState{
		model:    "test",
		opts:     Options{Chatter: chat},
		messages: seed,
	}

	if _, err := s.chat(context.Background(), nil); err != nil {
		t.Fatalf("chat() error: %v", err)
	}
	got := chat.requestsSeen()[0]
	if len(got) != len(seed) {
		t.Fatalf("provider saw %d messages for a paired transcript, want %d", len(got), len(seed))
	}
	for i := range seed {
		if got[i].Role != seed[i].Role || got[i].Content != seed[i].Content {
			t.Fatalf("message %d changed: %+v -> %+v", i, seed[i], got[i])
		}
	}
}
