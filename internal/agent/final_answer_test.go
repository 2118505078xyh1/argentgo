package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"example.com/argentgo/internal/llm"
)

func TestMaxTurnsAsksForOneToolFreeFinalAnswer(t *testing.T) {
	t.Setenv("ARGENT_CODE_MODE", "0") // 本测试精确断言工具集，隔离内建 code_mode
	tools := &fakeTools{specs: []llm.ToolSpec{toolSpec("read")}}
	chat := &fakeChatter{fn: func(n int, messages []llm.Message, specs []llm.ToolSpec, onText func(string)) (llm.Message, error) {
		if n <= 2 {
			if len(specs) != 1 {
				t.Errorf("regular turn %d advertised %d tools, want 1", n, len(specs))
			}
			return toolReply(toolCall(fmt.Sprintf("c%d", n), "read", `{}`)), nil
		}
		if len(specs) != 0 {
			t.Errorf("final request advertised %d tools, want none", len(specs))
		}
		if got := lastUserContent(messages); got != finalAnswerPrompt {
			t.Errorf("final request last user message = %q, want the synthetic prompt", got)
		}
		onText("final ")
		onText("answer")
		return textReply("final answer"), nil
	}}
	sink := &eventSink{}
	runner := &Runner{Opts: Options{
		Model: "m", Chatter: chat, Tools: tools, MaxTurns: 2, Emit: sink.emit,
	}}

	res, err := runner.Run(context.Background(), "loop")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Stopped != StopMaxTurns {
		t.Fatalf("stopped = %q, want %q", res.Stopped, StopMaxTurns)
	}
	if res.Turns != 2 {
		t.Fatalf("turns = %d, want 2 (the final request is not a turn)", res.Turns)
	}
	if chat.count() != 3 || tools.count() != 2 {
		t.Fatalf("requests = %d, tool calls = %d, want 3 and 2", chat.count(), tools.count())
	}
	if res.Text != "final answer" {
		t.Fatalf("text = %q, want the final answer", res.Text)
	}
	if !containsUser(res.Messages, finalAnswerPrompt) {
		t.Fatal("synthetic prompt is missing from the transcript")
	}
	assistants := 0
	for _, msg := range res.Messages {
		if msg.Role == "assistant" && msg.Content != "" {
			assistants++
		}
	}
	if assistants != 1 {
		t.Fatalf("assistant messages = %d, want 1 final answer", assistants)
	}
	last := res.Messages[len(res.Messages)-1]
	if last.Role != "assistant" || last.Content != "final answer" {
		t.Fatalf("last transcript entry = %+v, want the final answer", last)
	}
	if !sink.has(EventStop) {
		t.Fatal("no stop event was emitted")
	}
}

func TestFinalAnswerFailureStillStopsAtMaxTurns(t *testing.T) {
	tools := &fakeTools{specs: []llm.ToolSpec{toolSpec("read")}}
	chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n == 1 {
			return toolReply(toolCall("c1", "read", `{}`)), nil
		}
		return llm.Message{}, errors.New("provider unavailable")
	}}
	runner := &Runner{Opts: Options{Model: "m", Chatter: chat, Tools: tools, MaxTurns: 1}}

	res, err := runner.Run(context.Background(), "loop")
	if err != nil {
		t.Fatalf("a failed final answer must not fail the run: %v", err)
	}
	if res.Stopped != StopMaxTurns || res.Turns != 1 {
		t.Fatalf("result = %+v, want StopMaxTurns after 1 turn", res)
	}
	if containsUser(res.Messages, finalAnswerPrompt) {
		t.Fatal("a failed final request left a dangling synthetic user message")
	}
	for _, msg := range res.Messages {
		if msg.Role == "assistant" && msg.Content != "" {
			t.Fatalf("unexpected assistant answer after a failed final call: %q", msg.Content)
		}
	}
}

func TestNoFinalAnswerOptOutSkipsTheExtraRequest(t *testing.T) {
	tools := &fakeTools{specs: []llm.ToolSpec{toolSpec("read")}}
	chat := &fakeChatter{fn: func(int, []llm.Message, []llm.ToolSpec, func(string)) (llm.Message, error) {
		return toolReply(toolCall("c1", "read", `{}`)), nil
	}}
	runner := &Runner{Opts: Options{
		Model: "m", Chatter: chat, Tools: tools, MaxTurns: 1, NoFinalAnswer: true,
	}}

	res, err := runner.Run(context.Background(), "loop")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Stopped != StopMaxTurns || chat.count() != 1 || res.Turns != 1 {
		t.Fatalf("result = %+v, requests = %d, want StopMaxTurns after 1 request", res, chat.count())
	}
}

func TestFinalAnswerCancellationIsNotSwallowed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tools := &fakeTools{specs: []llm.ToolSpec{toolSpec("read")}}
	chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, onText func(string)) (llm.Message, error) {
		if n == 1 {
			return toolReply(toolCall("c1", "read", `{}`)), nil
		}
		// 在合成的最终答案请求流式中途中止。半写的文本绝不能到达操作人员的事件流。
		// （接上行）
		cancel()
		onText("half ")
		return llm.Message{}, ctx.Err()
	}}
	sink := &eventSink{}
	runner := &Runner{Opts: Options{Model: "m", Chatter: chat, Tools: tools, MaxTurns: 1, Emit: sink.emit}}

	res, err := runner.Run(ctx, "loop")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if res.Stopped != StopCancelled {
		t.Fatalf("stopped = %q, want %q", res.Stopped, StopCancelled)
	}
	if containsUser(res.Messages, finalAnswerPrompt) {
		t.Fatal("cancelled final answer left a dangling synthetic user message")
	}
	sink.mu.Lock()
	events := append([]Event(nil), sink.events...)
	sink.mu.Unlock()
	for _, ev := range events {
		if ev.Kind == EventTextDelta && strings.Contains(ev.Text, "half") {
			t.Fatal("partial final answer leaked as a text delta")
		}
	}
	stopReason := ""
	for _, ev := range events {
		if ev.Kind == EventStop {
			stopReason = ev.Reason
		}
	}
	if stopReason != StopCancelled {
		t.Fatalf("stop event reason = %q, want %q", stopReason, StopCancelled)
	}
	last := res.Messages[len(res.Messages)-1]
	if last.Role != "assistant" || last.Content != "Operation aborted" {
		t.Fatalf("last message = %+v, want the terminal aborted assistant", last)
	}
}

func TestMaxTurnsWithoutCancellationKeepsStopMaxTurns(t *testing.T) {
	tools := &fakeTools{specs: []llm.ToolSpec{toolSpec("read")}}
	chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, onText func(string)) (llm.Message, error) {
		if n == 1 {
			return toolReply(toolCall("c1", "read", `{}`)), nil
		}
		onText("final ")
		onText("answer")
		return textReply("final answer"), nil
	}}
	sink := &eventSink{}
	runner := &Runner{Opts: Options{Model: "m", Chatter: chat, Tools: tools, MaxTurns: 1, Emit: sink.emit}}

	res, err := runner.Run(context.Background(), "loop")
	if err != nil {
		t.Fatalf("a normal turn-limit stop must not error: %v", err)
	}
	if res.Stopped != StopMaxTurns {
		t.Fatalf("stopped = %q, want %q", res.Stopped, StopMaxTurns)
	}
	if res.Text != "final answer" || !sink.has(EventTextDelta) {
		t.Fatalf("text = %q, deltas emitted = %v, want a streamed final answer", res.Text, sink.has(EventTextDelta))
	}
}

func TestFilterSpecsNarrowsTheAdvertisedCatalogOnly(t *testing.T) {
	t.Setenv("ARGENT_CODE_MODE", "0") // 本测试精确断言过滤后的工具集，隔离内建 code_mode
	tools := &fakeTools{specs: []llm.ToolSpec{toolSpec("read"), toolSpec("write")}}
	chat := &fakeChatter{fn: func(int, []llm.Message, []llm.ToolSpec, func(string)) (llm.Message, error) {
		return textReply("ok"), nil
	}}
	runner := &Runner{Opts: Options{
		Model: "m", Chatter: chat, Tools: tools,
		FilterSpecs: func(specs []llm.ToolSpec) []llm.ToolSpec {
			out := make([]llm.ToolSpec, 0, len(specs))
			for _, spec := range specs {
				if spec.Function.Name != "write" {
					out = append(out, spec)
				}
			}
			return out
		},
	}}

	res, err := runner.Run(context.Background(), "inspect")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(chat.toolSets) != 1 || len(chat.toolSets[0]) != 1 || chat.toolSets[0][0].Function.Name != "read" {
		t.Fatalf("advertised tools = %+v, want only read", chat.toolSets)
	}
	if res.Stopped != StopComplete {
		t.Fatalf("stopped = %q, want complete", res.Stopped)
	}
}
