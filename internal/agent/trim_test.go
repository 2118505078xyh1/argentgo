package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argent-go/internal/llm"
)

func TestEstimateTokens(t *testing.T) {
	cases := []struct {
		text string
		want int
	}{
		{"", 0},
		{"a", 1},
		{"abcd", 1},
		{"abcde", 2},
		{strings.Repeat("x", 400), 100},
	}
	for _, tc := range cases {
		if got := EstimateTokens(tc.text); got != tc.want {
			t.Errorf("EstimateTokens(%d chars) = %d, want %d", len(tc.text), got, tc.want)
		}
	}
}

func TestEstimateMessageCountsOverheadAndCalls(t *testing.T) {
	plain := llm.Message{Role: "user", Content: strings.Repeat("x", 400)}
	if got := EstimateMessage(plain); got != 104 {
		t.Fatalf("estimate = %d, want 104 (100 text + 4 overhead)", got)
	}
	withCall := llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{toolCall("c", "read", `{"path":"a"}`)}}
	if got := EstimateMessage(withCall); got <= messageOverheadTokens {
		t.Fatalf("a tool call must add to the estimate, got %d", got)
	}
}

// trimFixture builds a conversation with an old tool result, a middle turn and
// the newest turn, all small except the tool bodies.
func trimFixture() []llm.Message {
	big := strings.Repeat("x", 4000)
	return []llm.Message{
		{Role: "system", Content: "system rules"},
		{Role: "user", Content: "first question"},
		{Role: "assistant", Content: "step one", ToolCalls: []llm.ToolCall{toolCall("c1", "read", `{}`)}},
		{Role: "tool", ToolCallID: "c1", Content: big},
		{Role: "assistant", Content: "step two", ToolCalls: []llm.ToolCall{toolCall("c2", "read", `{}`)}},
		{Role: "tool", ToolCallID: "c2", Content: big},
		{Role: "assistant", Content: "step three", ToolCalls: []llm.ToolCall{toolCall("c3", "read", `{}`)}},
		{Role: "tool", ToolCallID: "c3", Content: big},
	}
}

func TestTrimKeepsSystemFirstUserAndLastTurns(t *testing.T) {
	messages := trimFixture()
	before := EstimateMessages(messages)
	result := Trim(messages, TrimOptions{MaxTokens: 400, KeepTurns: 1, KeepToolTokens: 1})

	if len(result.Messages) != len(messages) {
		t.Fatalf("messages = %d, want %d: trimming must not remove entries", len(result.Messages), len(messages))
	}
	if result.Pruned == 0 {
		t.Fatal("expected at least one pruned tool result")
	}
	if result.Freed <= 0 {
		t.Fatalf("freed = %d, want > 0", result.Freed)
	}
	if after := EstimateMessages(result.Messages); after >= before {
		t.Fatalf("estimate did not shrink: %d -> %d", before, after)
	}
	if result.Messages[0].Content != "system rules" {
		t.Fatal("system prompt was trimmed")
	}
	if result.Messages[1].Content != "first question" {
		t.Fatal("first user message was trimmed")
	}
	if result.Messages[6].Content != "step three" {
		t.Fatal("last assistant turn was trimmed")
	}
	if result.Messages[7].Content == "" || strings.HasPrefix(result.Messages[7].Content, trimmedPrefix) {
		t.Fatal("most recent tool result was trimmed")
	}
	if !strings.HasPrefix(result.Messages[3].Content, trimmedPrefix) {
		t.Fatalf("oldest tool result = %q, want a stub", result.Messages[3].Content)
	}
	// Trimming twice must not re-stub an existing stub.
	again := Trim(result.Messages, TrimOptions{MaxTokens: 400, KeepTurns: 1, KeepToolTokens: 1})
	if again.Pruned != 0 {
		t.Fatalf("second pass pruned %d already-stubbed results", again.Pruned)
	}
}

func TestTrimKeepsLastNTurns(t *testing.T) {
	messages := trimFixture()
	result := Trim(messages, TrimOptions{MaxTokens: 400, KeepTurns: 3, KeepToolTokens: 1})
	for i := 4; i < len(messages); i++ {
		if result.Messages[i].Content != messages[i].Content {
			t.Fatalf("message %d inside the last 3 turns changed", i)
		}
	}
}

func TestTrimDoesNothingWhenUnderBudget(t *testing.T) {
	messages := trimFixture()
	result := Trim(messages, TrimOptions{MaxTokens: 100000})
	if result.Pruned != 0 {
		t.Fatalf("pruned %d results under budget", result.Pruned)
	}
}

func TestLoopTrimsOldToolOutput(t *testing.T) {
	big := strings.Repeat("y", 4000)
	tools := &fakeTools{
		specs: []llm.ToolSpec{toolSpec("read")},
		run: func(context.Context, string, string) (string, error) {
			return big, nil
		},
	}
	chat := &fakeChatter{fn: func(n int, messages []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n == 1 {
			return toolReply(toolCall("c1", "read", `{}`)), nil
		}
		if n < 5 {
			return toolReply(toolCall(fmt.Sprintf("c%d", n), "read", `{}`)), nil
		}
		return textReply("final"), nil
	}}
	runner := &Runner{Opts: Options{
		Model:            "m",
		Chatter:          chat,
		Tools:            tools,
		MaxTurns:         6,
		MaxContextTokens: 900,
		KeepTurns:        1,
		KeepToolTokens:   1,
	}}

	res, err := runner.Run(context.Background(), "explore")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Text != "final" {
		t.Fatalf("text = %q", res.Text)
	}
	trimmed := false
	for _, msg := range res.Messages {
		if strings.HasPrefix(msg.Content, trimmedPrefix) {
			trimmed = true
		}
	}
	if !trimmed {
		t.Fatal("no old tool result was trimmed")
	}
	for _, request := range chat.requestsSeen() {
		if request[0].Role != "system" {
			t.Fatal("system prompt missing from a request")
		}
		if request[1].Role != "user" || request[1].Content != "explore" {
			t.Fatal("first user message missing from a request")
		}
	}
}
