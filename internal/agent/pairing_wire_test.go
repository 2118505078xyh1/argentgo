package agent

import (
	"context"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// TestChatNormalizesToolPairingForProvider 检查 chat() 会修复交给 Chatter 的消息视图中孤立的 tool_use，
// 而不修改运行自身的记录（该修复仅在视图层）。
// （接上行）
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

	// 针对孤立调用的合成工具结果必须出现在视图中。
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
	// 运行的记录必须未被改动。
	if len(s.messages) != len(seed) || s.messages[2].Role != "assistant" {
		t.Fatalf("s.messages was mutated by the view repair: %+v", s.messages)
	}
}

// TestChatPassesPairedTranscriptUnchanged 检查完全配对的记录按引用原样到达 Chatter，未插入任何合成消息。
// （接上行）
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
