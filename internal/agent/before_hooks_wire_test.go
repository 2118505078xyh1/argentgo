package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// Durability hooks are injected per Runner through Options, so every test owns
// its own hooks. No process-global registry means these tests can run in
// parallel without resetting or leaking state into unrelated loop tests.
func TestBeforeRequestHookRunsBeforeChat(t *testing.T) {
	t.Parallel()

	requests, tools := 0, 0
	chat := &fakeChatter{fn: func(int, []llm.Message, []llm.ToolSpec, func(string)) (llm.Message, error) {
		return textReply("done"), nil
	}}
	runner := &Runner{Opts: Options{
		Model: "m", Chatter: chat, Tools: &fakeTools{},
		BeforeRequest: func() error { requests++; return nil },
		BeforeTool:    func() error { tools++; return nil },
	}}

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
	t.Parallel()

	toolsRun := 0
	tools := &fakeTools{specs: []llm.ToolSpec{toolSpec("read")}}
	chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n == 1 {
			return toolReply(toolCall("c1", "read", `{}`)), nil
		}
		return textReply("done"), nil
	}}
	runner := &Runner{Opts: Options{
		Model: "m", Chatter: chat, Tools: tools,
		BeforeRequest: func() error { return nil },
		BeforeTool:    func() error { toolsRun++; return nil },
	}}

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
	t.Parallel()

	wantErr := errors.New("session not durable")
	chat := &fakeChatter{fn: func(int, []llm.Message, []llm.ToolSpec, func(string)) (llm.Message, error) {
		t.Fatal("chatter must not be called when the before-request hook fails")
		return llm.Message{}, nil
	}}
	runner := &Runner{Opts: Options{
		Model: "m", Chatter: chat, Tools: &fakeTools{},
		BeforeRequest: func() error { return wantErr },
	}}

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
	t.Parallel()

	tools := &fakeTools{specs: []llm.ToolSpec{toolSpec("read")}}
	chat := &fakeChatter{fn: func(int, []llm.Message, []llm.ToolSpec, func(string)) (llm.Message, error) {
		return toolReply(toolCall("c1", "read", `{}`)), nil
	}}
	runner := &Runner{Opts: Options{
		Model: "m", Chatter: chat, Tools: tools,
		BeforeTool: func() error { return errors.New("not durable yet") },
	}}

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

// TestBeforeHooksArePerRunner proves two concurrent runs cannot see or clear
// each other's durability barriers: one runner's hook errors while the other's
// succeeds, and neither result leaks into the other. This was impossible with
// the old package-global registry (a Reset from one run cleared the other).
func TestBeforeHooksArePerRunner(t *testing.T) {
	t.Parallel()

	blocked := errors.New("not durable")
	newRunner := func(before func() error) *Runner {
		return &Runner{Opts: Options{
			Model: "m",
			Chatter: &fakeChatter{fn: func(int, []llm.Message, []llm.ToolSpec, func(string)) (llm.Message, error) {
				return textReply("done"), nil
			}},
			Tools:         &fakeTools{},
			BeforeRequest: before,
		}}
	}

	blockedRes, blockedErr := newRunner(func() error { return blocked }).Run(context.Background(), "a")
	okRes, okErr := newRunner(nil).Run(context.Background(), "b")

	if !errors.Is(blockedErr, blocked) {
		t.Fatalf("blocked runner err = %v, want %v", blockedErr, blocked)
	}
	if okErr != nil {
		t.Fatalf("unconfigured runner err = %v, want nil", okErr)
	}
	if okRes.Stopped != StopComplete {
		t.Fatalf("unconfigured runner stopped = %q, want %q", okRes.Stopped, StopComplete)
	}
	if blockedRes.Stopped != StopProviderError {
		t.Fatalf("blocked runner stopped = %q, want %q", blockedRes.Stopped, StopProviderError)
	}
}
