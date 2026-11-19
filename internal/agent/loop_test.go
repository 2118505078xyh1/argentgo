package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

func TestToolThenAnswer(t *testing.T) {
	tools := &fakeTools{specs: []llm.ToolSpec{toolSpec("read")}}
	chat := &fakeChatter{fn: func(n int, messages []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n == 1 {
			return toolReply(toolCall("c1", "read", `{"path":"a.txt"}`)), nil
		}
		if got := lastToolContent(messages); got != "read ok" {
			t.Fatalf("tool result = %q, want %q", got, "read ok")
		}
		return textReply("done"), nil
	}}
	runner := &Runner{Opts: Options{Model: "m", Chatter: chat, Tools: tools}}

	res, err := runner.Run(context.Background(), "read a.txt")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Text != "done" || res.Stopped != StopComplete {
		t.Fatalf("result = %+v, want text done and stop %q", res, StopComplete)
	}
	if res.Turns != 2 {
		t.Fatalf("turns = %d, want 2", res.Turns)
	}
	if tools.count() != 1 {
		t.Fatalf("tool calls = %d, want 1", tools.count())
	}
	if len(res.Messages) != 5 {
		t.Fatalf("messages = %d, want 5 (system, user, assistant, tool, assistant)", len(res.Messages))
	}
}

func TestToolErrorIsReturnedToTheModel(t *testing.T) {
	tools := &fakeTools{
		specs: []llm.ToolSpec{toolSpec("read")},
		run: func(context.Context, string, string) (string, error) {
			return "", errors.New("boom")
		},
	}
	chat := &fakeChatter{fn: func(n int, messages []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n == 1 {
			return toolReply(toolCall("c1", "read", `{}`)), nil
		}
		if got := lastToolContent(messages); !strings.Contains(got, "error: boom") {
			t.Fatalf("tool result = %q, want the error text", got)
		}
		return textReply("recovered"), nil
	}}
	runner := &Runner{Opts: Options{Model: "m", Chatter: chat, Tools: tools}}

	res, err := runner.Run(context.Background(), "read")
	if err != nil {
		t.Fatalf("run aborted on a tool error: %v", err)
	}
	if res.Text != "recovered" || res.Stopped != StopComplete {
		t.Fatalf("result = %+v, want recovery", res)
	}
}

func TestApprovalDenialSkipsTheTool(t *testing.T) {
	tools := &fakeTools{specs: []llm.ToolSpec{toolSpec("write")}}
	asks := 0
	approve := approverFunc(func(context.Context, string, string) (bool, error) {
		asks++
		return false, nil
	})
	chat := &fakeChatter{fn: func(n int, messages []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n == 1 {
			return toolReply(toolCall("c1", "write", `{"path":"a.txt"}`)), nil
		}
		if got := lastToolContent(messages); !strings.Contains(got, "denied") {
			t.Fatalf("tool result = %q, want a denial", got)
		}
		return textReply("understood"), nil
	}}
	runner := &Runner{Opts: Options{
		Model:         "m",
		Chatter:       chat,
		Tools:         tools,
		Approve:       approve,
		MutatingTools: []string{"write"},
	}}

	res, err := runner.Run(context.Background(), "write a.txt")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Stopped != StopComplete || res.Text != "understood" {
		t.Fatalf("result = %+v, want a completed run", res)
	}
	if tools.count() != 0 {
		t.Fatalf("tool ran %d time(s) after denial", tools.count())
	}
	if asks != 1 {
		t.Fatalf("approvals = %d, want 1", asks)
	}
}

func TestApprovalGrantsMutatingTool(t *testing.T) {
	tools := &fakeTools{specs: []llm.ToolSpec{toolSpec("write")}}
	chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n == 1 {
			return toolReply(toolCall("c1", "write", `{}`)), nil
		}
		return textReply("done"), nil
	}}
	runner := &Runner{Opts: Options{
		Model:    "m",
		Chatter:  chat,
		Tools:    tools,
		Approve:  approverFunc(func(context.Context, string, string) (bool, error) { return true, nil }),
		Mutating: func(string) bool { return true },
	}}
	if _, err := runner.Run(context.Background(), "write"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if tools.count() != 1 {
		t.Fatalf("tool calls = %d, want 1", tools.count())
	}
}

func TestTurnLimitStops(t *testing.T) {
	tools := &fakeTools{specs: []llm.ToolSpec{toolSpec("read")}}
	chat := &fakeChatter{fn: func(int, []llm.Message, []llm.ToolSpec, func(string)) (llm.Message, error) {
		return toolReply(toolCall("c1", "read", `{}`)), nil
	}}
	runner := &Runner{Opts: Options{Model: "m", Chatter: chat, Tools: tools, MaxTurns: 2}}

	res, err := runner.Run(context.Background(), "loop")
	if err != nil {
		t.Fatalf("turn limit should not be an error: %v", err)
	}
	if res.Stopped != StopMaxTurns {
		t.Fatalf("stopped = %q, want %q", res.Stopped, StopMaxTurns)
	}
	if res.Turns != 2 {
		t.Fatalf("turns = %d, want 2", res.Turns)
	}
}

func TestCancellationBeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	chat := &fakeChatter{fn: func(int, []llm.Message, []llm.ToolSpec, func(string)) (llm.Message, error) {
		t.Fatal("chatter must not be called on a cancelled run")
		return llm.Message{}, nil
	}}
	runner := &Runner{Opts: Options{Model: "m", Chatter: chat, Tools: &fakeTools{}}}

	res, err := runner.Run(ctx, "hi")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if res.Stopped != StopCancelled {
		t.Fatalf("stopped = %q, want %q", res.Stopped, StopCancelled)
	}
}

func TestCancellationMidRunSkipsTheTool(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tools := &fakeTools{specs: []llm.ToolSpec{toolSpec("read")}}
	chat := &fakeChatter{fn: func(int, []llm.Message, []llm.ToolSpec, func(string)) (llm.Message, error) {
		cancel()
		return toolReply(toolCall("c1", "read", `{}`)), nil
	}}
	runner := &Runner{Opts: Options{Model: "m", Chatter: chat, Tools: tools}}

	res, err := runner.Run(ctx, "read")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if res.Stopped != StopCancelled {
		t.Fatalf("stopped = %q, want %q", res.Stopped, StopCancelled)
	}
	if tools.count() != 0 {
		t.Fatalf("tool ran after cancellation")
	}
	if got := lastToolContent(res.Messages); !strings.Contains(got, "cancelled") {
		t.Fatalf("unrun call has result %q, want a cancellation stub", got)
	}
}

func TestPromptForwardsDeltas(t *testing.T) {
	chat := &fakeChatter{fn: func(_ int, _ []llm.Message, _ []llm.ToolSpec, onText func(string)) (llm.Message, error) {
		onText("hel")
		onText("lo")
		return textReply("hello"), nil
	}}
	runner := &Runner{Opts: Options{Model: "m", Chatter: chat, Tools: &fakeTools{}}}

	var streamed strings.Builder
	text, err := runner.Prompt(context.Background(), "hi", func(delta string) { streamed.WriteString(delta) })
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if text != "hello" || streamed.String() != "hello" {
		t.Fatalf("text = %q, streamed = %q", text, streamed.String())
	}
}

// approverFunc adapts a function to Approver.
type approverFunc func(ctx context.Context, name, args string) (bool, error)

func (f approverFunc) Ask(ctx context.Context, name, args string) (bool, error) {
	return f(ctx, name, args)
}
