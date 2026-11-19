package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// The before-hooks are package-global, so every test here resets them on entry
// and on exit to avoid leaking hooks into unrelated loop tests.
func TestBeforeRequestHookRunsBeforeChat(t *testing.T) {
	ResetBeforeHooks()
	defer ResetBeforeHooks()

	requests, tools := 0, 0
	RegisterBeforeRequest(func() error { requests++; return nil })
	RegisterBeforeTool(func() error { tools++; return nil })

	chat := &fakeChatter{fn: func(int, []llm.Message, []llm.ToolSpec, func(string)) (llm.Message, error) {
		return textReply("done"), nil
	}}
	runner := &Runner{Opts: Options{Model: "m", Chatter: chat, Tools: &fakeTools{}}}

	res, err := runner.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Stopped != StopComplete {
		t.Fatalf("stopped = %q, want %q", res.Stopped, StopComplete)
	}
	if requests < 1 {
		t.Fatalf("before-request hook called %d time(s), want at least 1", requests)
	}
	// No tool call occurred, so the tool hook must not have run.
	if tools != 0 {
		t.Fatalf("before-tool hook called %d time(s) with no tool call", tools)
	}
}

func TestBeforeToolHookRunsBeforeDispatch(t *testing.T) {
	ResetBeforeHooks()
	defer ResetBeforeHooks()

	toolsRun := 0
	RegisterBeforeRequest(func() error { return nil })
	RegisterBeforeTool(func() error { toolsRun++; return nil })

	tools := &fakeTools{specs: []llm.ToolSpec{toolSpec("read")}}
	chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n == 1 {
			return toolReply(toolCall("c1", "read", `{}`)), nil
		}
		return textReply("done"), nil
	}}
	runner := &Runner{Opts: Options{Model: "m", Chatter: chat, Tools: tools}}

	res, err := runner.Run(context.Background(), "read")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Stopped != StopComplete {
		t.Fatalf("stopped = %q, want %q", res.Stopped, StopComplete)
	}
	if toolsRun < 1 {
		t.Fatalf("before-tool hook never ran before a real tool call")
	}
	if tools.count() != 1 {
		t.Fatalf("tool calls = %d, want 1", tools.count())
	}
}

func TestBeforeRequestHookErrorStopsRunBeforeChat(t *testing.T) {
	ResetBeforeHooks()
	defer ResetBeforeHooks()

	wantErr := errors.New("session not durable")
	RegisterBeforeRequest(func() error { return wantErr })

	chat := &fakeChatter{fn: func(int, []llm.Message, []llm.ToolSpec, func(string)) (llm.Message, error) {
		t.Fatal("chatter must not be called when the before-request hook fails")
		return llm.Message{}, nil
	}}
	runner := &Runner{Opts: Options{Model: "m", Chatter: chat, Tools: &fakeTools{}}}

	res, err := runner.Run(context.Background(), "hi")
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if res.Stopped != StopProviderError {
		t.Fatalf("stopped = %q, want %q", res.Stopped, StopProviderError)
	}
	if chat.count() != 0 {
		t.Fatalf("chatter called %d time(s) after a hook error", chat.count())
	}
}

func TestBeforeToolHookErrorFillsUnrunWithoutExecuting(t *testing.T) {
	ResetBeforeHooks()
	defer ResetBeforeHooks()

	RegisterBeforeTool(func() error { return errors.New("not durable yet") })

	tools := &fakeTools{specs: []llm.ToolSpec{toolSpec("read")}}
	chat := &fakeChatter{fn: func(int, []llm.Message, []llm.ToolSpec, func(string)) (llm.Message, error) {
		return toolReply(toolCall("c1", "read", `{}`)), nil
	}}
	runner := &Runner{Opts: Options{Model: "m", Chatter: chat, Tools: tools}}

	res, err := runner.Run(context.Background(), "read")
	if err != nil {
		t.Fatalf("hook errors become tool results, not a run error: %v", err)
	}
	if tools.count() != 0 {
		t.Fatalf("tool ran %d time(s) after a hook error", tools.count())
	}
	if got := lastToolContent(res.Messages); !strings.Contains(got, "tool dispatch aborted") {
		t.Fatalf("unrun tool result = %q, want the hook error reason", got)
	}
	if len(res.Messages) < 4 || res.Messages[len(res.Messages)-1].Role != "tool" {
		t.Fatalf("transcript does not end with the unrun tool result: %+v", res.Messages)
	}
}
