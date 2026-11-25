package agent

import (
	"context"
	"strings"
	"testing"

	"example.com/argentgo/internal/llm"
)

func TestSplitInvalidArgCalls(t *testing.T) {
	malformed := llm.ToolCall{ID: "a", Function: llm.ToolCallFunction{Name: "write", Arguments: "{}"}}
	malformed.InvalidArguments = `{"path":`
	valid := llm.ToolCall{ID: "b", Function: llm.ToolCallFunction{Name: "read", Arguments: `{"path":"x"}`}}

	t.Run("off keeps all calls and no invalid", func(t *testing.T) {
		SetToolArgFeedback(false)
		defer SetToolArgFeedback(false)
		invalid, rest := splitInvalidArgCalls([]llm.ToolCall{malformed, valid})
		if len(invalid) != 0 || len(rest) != 2 {
			t.Fatalf("off: invalid=%d rest=%d", len(invalid), len(rest))
		}
	})

	t.Run("on separates malformed from valid", func(t *testing.T) {
		SetToolArgFeedback(true)
		defer SetToolArgFeedback(false)
		invalid, rest := splitInvalidArgCalls([]llm.ToolCall{malformed, valid})
		if len(invalid) != 1 || invalid[0].ID != "a" {
			t.Fatalf("on: invalid=%+v", invalid)
		}
		if len(rest) != 1 || rest[0].ID != "b" {
			t.Fatalf("on: rest=%+v", rest)
		}
	})
}

func TestRunToolsDoesNotExecuteMalformedArgs(t *testing.T) {
	var ran []string
	ft := &fakeTools{run: func(_ context.Context, name, _ string) (string, error) {
		ran = append(ran, name)
		return "ok", nil
	}}
	s := newRunState(Options{Tools: ft}, "go", nil)

	malformed := toolCall("c1", "write", "{}")
	malformed.InvalidArguments = `{"path":` // 截断的 JSON
	valid := toolCall("c2", "read", `{"path":"x"}`)

	SetToolArgFeedback(true)
	defer SetToolArgFeedback(false)
	if got := s.runTools(context.Background(), 0, []llm.ToolCall{malformed, valid}); got != "" {
		t.Fatalf("runTools stop = %q, want empty", got)
	}
	// 只有合法调用真正运行；畸形调用被拦截。
	if len(ran) != 1 || ran[0] != "read" {
		t.Fatalf("tools run = %v, want only read", ran)
	}
	// 回灌消息含纠错提示与原始片段，且状态中保留配对。
	joined := ""
	for _, m := range s.messages {
		joined += m.Content
	}
	if !strings.Contains(joined, "malformed") || !strings.Contains(joined, "raw input") || !strings.Contains(joined, "path") {
		t.Fatalf("feedback missing guidance or raw fragment: %s", joined)
	}
}

func TestInvalidArgsReasonRedactsAndLimits(t *testing.T) {
	SetToolArgFeedback(false)
	call := llm.ToolCall{InvalidArguments: `{"key":"sk-abcdefghijklmnop1234","pad":"` + strings.Repeat("字", 400) + `"}`}
	reason := invalidArgsReason(call)
	if strings.Contains(reason, "sk-abcdefghijklmnop1234") {
		t.Fatalf("secret leaked into reason: %s", reason)
	}
	if !strings.Contains(reason, "malformed") || !strings.Contains(reason, "raw input") {
		t.Fatalf("reason missing guidance: %s", reason)
	}
	if len([]rune(reason)) > invalidArgRawLimit+120 {
		t.Fatalf("reason not bounded: %d runes", len([]rune(reason)))
	}
}
