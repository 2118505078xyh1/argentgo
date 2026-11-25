package agent

import (
	"context"
	"strings"
	"testing"

	"example.com/argentgo/internal/llm"
)

func TestNormalizeArgs(t *testing.T) {
	cases := map[string]string{
		`{"b":2,"a":1}`:              `{"a":1,"b":2}`,
		`{ "a" : {"y": 1, "x": 2} }`: `{"a":{"x":2,"y":1}}`,
		``:                           `{}`,
		`not json`:                   `not json`,
	}
	for input, want := range cases {
		if got := NormalizeArgs(input); got != want {
			t.Errorf("NormalizeArgs(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestDedupeTripsAfterTheAllowance(t *testing.T) {
	guard := newDedupe(2)
	for i := 0; i < 2; i++ {
		if blocked, msg := guard.check("read", `{"path": "a"}`); blocked {
			t.Fatalf("call %d blocked too early: %s", i+1, msg)
		}
	}
	blocked, msg := guard.check("read", `{"path":"a"}`)
	if !blocked {
		t.Fatal("third identical call should be blocked")
	}
	if !strings.Contains(msg, "duplicate call blocked") {
		t.Fatalf("message = %q", msg)
	}
	if blocked, _ := guard.check("read", `{"path":"b"}`); blocked {
		t.Fatal("different arguments must not be blocked")
	}
	if blocked, _ := guard.check("grep", `{"path":"a"}`); blocked {
		t.Fatal("different tool must not be blocked")
	}
}

func TestDedupeBlocksRepeatsInLoop(t *testing.T) {
	tools := &fakeTools{specs: []llm.ToolSpec{toolSpec("read")}}
	chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n%2 == 1 {
			return toolReply(toolCall("c", "read", `{"path":"a.txt"}`)), nil
		}
		return toolReply(toolCall("c", "read", `{"path": "a.txt"}`)), nil
	}}
	runner := &Runner{Opts: Options{
		Model:      "m",
		Chatter:    chat,
		Tools:      tools,
		MaxTurns:   4,
		MaxRepeats: 2,
	}}

	res, err := runner.Run(context.Background(), "read repeatedly")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if tools.count() != 2 {
		t.Fatalf("real tool calls = %d, want 2", tools.count())
	}
	blocked := false
	for _, msg := range res.Messages {
		if msg.Role == "tool" && strings.Contains(msg.Content, "duplicate call blocked") {
			blocked = true
		}
	}
	if !blocked {
		t.Fatal("no duplicate-call error result in the transcript")
	}
	if res.Stopped != StopMaxTurns {
		t.Fatalf("stopped = %q, want %q", res.Stopped, StopMaxTurns)
	}
}
