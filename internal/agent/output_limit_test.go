package agent

import (
	"context"
	"strings"
	"testing"

	"example.com/argentgo/internal/llm"
)

// TestOutputLimitContinuation 检查循环如何处理因输出长度而非正常结束的无工具回复。
// （接上行）
func TestOutputLimitContinuation(t *testing.T) {
	lengthReply := func(text string) llm.Message {
		reply := textReply(text)
		reply.FinishReason = "length"
		return reply
	}
	stopReply := func(text string) llm.Message {
		reply := textReply(text)
		reply.FinishReason = "stop"
		return reply
	}
	truncatedToolReply := func() llm.Message {
		reply := toolReply(toolCall("c1", "read", `{}`))
		reply.FinishReason = "max_tokens"
		return reply
	}

	tests := []struct {
		name              string
		replies           []llm.Message
		wantStopped       string
		wantTurns         int
		wantContinuations int
	}{
		{
			name:        "normal stop completes without continuation",
			replies:     []llm.Message{stopReply("done")},
			wantStopped: StopComplete,
			wantTurns:   1,
		},
		{
			name:              "one length finish appends a continuation and the run continues",
			replies:           []llm.Message{lengthReply("part one"), stopReply("part two")},
			wantStopped:       StopComplete,
			wantTurns:         2,
			wantContinuations: 1,
		},
		{
			name:              "two consecutive length finishes stop with output limit",
			replies:           []llm.Message{lengthReply("part one"), lengthReply("part two")},
			wantStopped:       StopOutputLimit,
			wantTurns:         2,
			wantContinuations: 1,
		},
		{
			name: "truncated tool reply counts toward the continuation budget",
			replies: []llm.Message{
				lengthReply("part one"),
				truncatedToolReply(),
				stopReply("finished"),
			},
			wantStopped:       StopComplete,
			wantTurns:         3,
			wantContinuations: 1,
		},
		{
			name: "non length tool reply resets the continuation budget",
			replies: []llm.Message{
				lengthReply("part one"),
				toolReply(toolCall("c1", "read", `{}`)),
				lengthReply("part three"),
				stopReply("finished"),
			},
			wantStopped:       StopComplete,
			wantTurns:         4,
			wantContinuations: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tools := &fakeTools{specs: []llm.ToolSpec{toolSpec("read")}}
			chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
				if n > len(tt.replies) {
					t.Fatalf("unexpected request %d (script has %d replies)", n, len(tt.replies))
				}
				return tt.replies[n-1], nil
			}}
			sink := &eventSink{}
			runner := &Runner{Opts: Options{Model: "m", Chatter: chat, Tools: tools, Emit: sink.emit}}

			res, err := runner.Run(context.Background(), "work")
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if res.Stopped != tt.wantStopped {
				t.Fatalf("stopped = %q, want %q", res.Stopped, tt.wantStopped)
			}
			if res.Turns != tt.wantTurns || chat.count() != tt.wantTurns {
				t.Fatalf("turns = %d, requests = %d, want %d", res.Turns, chat.count(), tt.wantTurns)
			}
			if got := countUser(res.Messages, continueAfterOutputLimit); got != tt.wantContinuations {
				t.Fatalf("continuation messages = %d, want %d: %+v", got, tt.wantContinuations, res.Messages)
			}

			// 继续指令必须在紧接着的下一次请求中可见：这正是让模型获得新的输出预算以完成工作的机制。
			// （接上行）
			seen := chat.requestsSeen()
			for i := 1; i < len(seen); i++ {
				prev := tt.replies[i-1]
				if prev.FinishReason == "length" && len(prev.ToolCalls) == 0 &&
					!containsUser(seen[i], continueAfterOutputLimit) {
					t.Fatalf("request %d did not see the continuation prompt", i+1)
				}
			}
			if tt.wantContinuations > 0 {
				announced := false
				for _, event := range sink.events {
					if event.Kind == EventNotice && strings.Contains(event.Text, "output limit") {
						announced = true
					}
				}
				if !announced {
					t.Fatal("no notice announced the output-limit continuation")
				}
			}
		})
	}
}
