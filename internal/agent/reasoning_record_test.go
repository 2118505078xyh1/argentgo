package agent

import (
	"context"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// TestAssistantRecordCarriesReasoning locks step 3 of the reasoning replay
// chain: when a provider reply carries reasoning, the assistant record written
// through the injected Recorder is the raw llm.Message with Reasoning intact,
// and the in-memory transcript keeps it too (so later turns can replay it).
func TestAssistantRecordCarriesReasoning(t *testing.T) {
	const reasoning = "逐步思考：先读文件，再修改，最后给出答案。"
	chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		return llm.Message{Role: "assistant", Content: "done", Reasoning: reasoning}, nil
	}}

	type row struct {
		kind    string
		payload any
	}
	var rows []row
	rec := RecorderFunc(func(kind string, payload any) error {
		rows = append(rows, row{kind: kind, payload: payload})
		return nil
	})
	runner := &Runner{Opts: Options{Model: "m", Chatter: chat, Record: rec}}
	result, err := runner.Run(context.Background(), "task")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	found := false
	for _, r := range rows {
		if r.kind != "assistant" {
			continue
		}
		msg, ok := r.payload.(llm.Message)
		if !ok {
			t.Fatalf("assistant payload = %T, want llm.Message", r.payload)
		}
		if msg.Reasoning == reasoning {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no assistant record carried reasoning %q; rows = %+v", reasoning, rows)
	}

	// The transcript view handed back to the caller keeps reasoning as well,
	// which is what an opt-in wire replay reads on the next turn.
	transcriptHasReasoning := false
	for _, m := range result.Messages {
		if m.Role == "assistant" && m.Reasoning == reasoning {
			transcriptHasReasoning = true
			break
		}
	}
	if !transcriptHasReasoning {
		t.Fatalf("result transcript lost reasoning: %+v", result.Messages)
	}
}

// TestReasoningSurvivesToolPairingView verifies the outgoing view used before
// each request does not strip reasoning from assistant tool-use turns.
func TestReasoningSurvivesToolPairingView(t *testing.T) {
	const reasoning = "中文推理内容"
	messages := []llm.Message{
		{Role: "user", Content: "task"},
		{
			Role:      "assistant",
			ToolCalls: []llm.ToolCall{{ID: "c1", Type: "function", Function: llm.ToolCallFunction{Name: "read", Arguments: `{}`}}},
			Reasoning: reasoning,
		},
		{Role: "tool", ToolCallID: "c1", Content: "result"},
	}
	view := NormalizeToolPairing(messages)
	if len(view) != 3 {
		t.Fatalf("view = %d messages, want 3", len(view))
	}
	if view[1].Reasoning != reasoning {
		t.Fatalf("view reasoning = %q, want %q", view[1].Reasoning, reasoning)
	}
}
