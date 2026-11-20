package llm

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// reasoningMessages is a mixed conversation: only assistant turns carry
// reasoning, and one assistant turn has tool calls instead of text.
func reasoningMessages() []Message {
	return []Message{
		{Role: "system", Content: "rules"},
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "answer", Reasoning: "思考过程"},
		{Role: "tool", ToolCallID: "c1", Content: "result"},
		{
			Role:      "assistant",
			ToolCalls: []ToolCall{{ID: "c1", Type: "function", Function: ToolCallFunction{Name: "read", Arguments: `{}`}}},
			Reasoning: "再想想",
		},
	}
}

// TestWireMessagesDefaultOmitsReasoning locks the hard requirement: with
// PreserveReasoning off, wireMessages produces exactly the same []chatMessage
// bytes as before the reasoning field existed.
func TestWireMessagesDefaultOmitsReasoning(t *testing.T) {
	messages := reasoningMessages()
	got := wireMessages(messages, false, nil, false)
	want := []chatMessage{
		{Role: "system", Content: "rules"},
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "answer"},
		{Role: "tool", ToolCallID: "c1", Content: "result"},
		{
			Role:      "assistant",
			ToolCalls: []ToolCall{{ID: "c1", Type: "function", Function: ToolCallFunction{Name: "read", Arguments: `{}`}}},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("wireMessages(default) = %#v, want %#v", got, want)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "reasoning") {
		t.Fatalf("default wire bytes contain reasoning: %s", raw)
	}
}

// TestWireMessagesPreserveReasoningOptIn checks that only assistant turns get
// reasoning_content, that user/tool/system turns never do, and that the CJK
// reasoning text is copied verbatim.
func TestWireMessagesPreserveReasoningOptIn(t *testing.T) {
	got := wireMessages(reasoningMessages(), false, nil, true)
	if got[2].ReasoningContent != "思考过程" {
		t.Fatalf("assistant reasoning_content = %q, want 思考过程", got[2].ReasoningContent)
	}
	if got[4].ReasoningContent != "再想想" {
		t.Fatalf("tool-assistant reasoning_content = %q, want 再想想", got[4].ReasoningContent)
	}
	for _, i := range []int{0, 1, 3} {
		if got[i].ReasoningContent != "" {
			t.Fatalf("message %d (%s) must not carry reasoning, got %q", i, got[i].Role, got[i].ReasoningContent)
		}
	}
}

// TestAnthropicBuildRequestReasoningOptIn verifies the thinking block is
// emitted only when PreserveReasoning is on, and that the default request is
// byte-identical to a build without reasoning.
func TestAnthropicBuildRequestReasoningOptIn(t *testing.T) {
	messages := reasoningMessages()
	client := &AnthropicClient{}

	defaultReq := client.buildRequest("m", messages, nil)
	defaultJSON, err := json.Marshal(defaultReq)
	if err != nil {
		t.Fatalf("marshal default: %v", err)
	}
	if strings.Contains(string(defaultJSON), "thinking") {
		t.Fatalf("default Anthropic request contains thinking: %s", defaultJSON)
	}

	client.Options.PreserveReasoning = true
	req := client.buildRequest("m", messages, nil)
	// Assistant turns are req.Messages[1] and [3]; each must start with a
	// thinking block carrying the exact reasoning text.
	for _, tc := range []struct {
		index int
		want  string
	}{{1, "思考过程"}, {3, "再想想"}} {
		blocks := req.Messages[tc.index].Content
		if len(blocks) == 0 || blocks[0].Type != "thinking" || blocks[0].Thinking != tc.want {
			t.Fatalf("assistant %d blocks = %+v, want leading thinking %q", tc.index, blocks, tc.want)
		}
	}
	for _, index := range []int{0, 2} {
		for _, block := range req.Messages[index].Content {
			if block.Type == "thinking" {
				t.Fatalf("message %d must not carry a thinking block", index)
			}
		}
	}
}
