package agent

import (
	"context"
	"testing"
	"time"

	"example.com/argentgo/internal/llm"
)

func TestParallelBatchKeepsCallOrder(t *testing.T) {
	sink := &eventSink{}
	tools := &fakeTools{
		specs: []llm.ToolSpec{toolSpec("read"), toolSpec("grep"), toolSpec("ls")},
		run: func(_ context.Context, name, _ string) (string, error) {
			// 第一个调用最后完成：结果仍必须按调用顺序记录。
			// （接上行）
			switch name {
			case "read":
				time.Sleep(60 * time.Millisecond)
			case "grep":
				time.Sleep(30 * time.Millisecond)
			}
			return "out-" + name, nil
		},
	}
	chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n == 1 {
			return toolReply(
				toolCall("c1", "read", `{}`),
				toolCall("c2", "grep", `{}`),
				toolCall("c3", "ls", `{}`),
			), nil
		}
		return textReply("done"), nil
	}}
	runner := &Runner{Opts: Options{
		Model:       "m",
		Chatter:     chat,
		Tools:       tools,
		MaxTurns:    2,
		MaxParallel: 3,
		Emit:        sink.emit,
	}}

	res, err := runner.Run(context.Background(), "inspect")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Text != "done" {
		t.Fatalf("text = %q", res.Text)
	}
	want := []struct{ id, output string }{
		{"c1", "out-read"},
		{"c2", "out-grep"},
		{"c3", "out-ls"},
	}
	var got []llm.Message
	for _, msg := range res.Messages {
		if msg.Role == "tool" {
			got = append(got, msg)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("tool results = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].ToolCallID != want[i].id || got[i].Content != want[i].output {
			t.Fatalf("result %d = %+v, want id %q output %q", i, got[i], want[i].id, want[i].output)
		}
	}

	// 每个调用都在第一个结果到达之前被宣告。
	starts, ends := 0, 0
	for _, event := range sink.events {
		switch event.Kind {
		case EventToolStart:
			if ends > 0 {
				t.Fatal("a tool started after another finished; the batch should be announced up front")
			}
			starts++
		case EventToolEnd:
			ends++
		}
	}
	if starts != 3 || ends != 3 {
		t.Fatalf("tool events = %d start / %d end, want 3/3", starts, ends)
	}
}

func TestParallelIsNotUsedForMutatingCalls(t *testing.T) {
	tools := &fakeTools{specs: []llm.ToolSpec{toolSpec("write"), toolSpec("read")}}
	chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n == 1 {
			return toolReply(
				toolCall("c1", "write", `{}`),
				toolCall("c2", "read", `{}`),
			), nil
		}
		return textReply("done"), nil
	}}
	runner := &Runner{Opts: Options{
		Model:         "m",
		Chatter:       chat,
		Tools:         tools,
		MaxTurns:      2,
		MaxParallel:   4,
		MutatingTools: []string{"write"},
	}}

	res, err := runner.Run(context.Background(), "mix")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if names := tools.names(); len(names) != 2 || names[0] != "write" || names[1] != "read" {
		t.Fatalf("tool order = %v, want write then read", names)
	}
	if res.Stopped != StopComplete {
		t.Fatalf("stopped = %q", res.Stopped)
	}
}
