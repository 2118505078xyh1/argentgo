package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// 这些辅助函数编排循环必须纠正的两类无工具回复，以及一个必须降级为停止的畸形工具轮次。
// （接上行）
func noContentReply(finish string) llm.Message {
	return llm.Message{Role: "assistant", FinishReason: finish}
}

func reasoningOnlyReply(text string) llm.Message {
	return llm.Message{Role: "assistant", Reasoning: text, FinishReason: "stop"}
}

func downgradedToolReply(finish string) llm.Message {
	return llm.Message{Role: "assistant", FinishReason: finish}
}

// TestEmptyReplyCorrectionWiring 锁定回复四分类在循环层的行为：空回复和纯推理回复获得有限的回答机会，
// 而正常答案、工具批次和被降级的工具轮次保持其原有的停止语义。
// （接上行）
// （接上行）
func TestEmptyReplyCorrectionWiring(t *testing.T) {
	empty := noContentReply("stop")
	reasoning := reasoningOnlyReply("thinking about it")
	lengthEmpty := noContentReply("length")
	truncatedTool := toolReply(toolCall("c1", "read", `{}`))
	truncatedTool.FinishReason = "max_tokens"

	tests := []struct {
		name            string
		replies         []llm.Message
		wantStopped     string
		wantTurns       int
		wantCorrections int
		wantToolCalls   int
		// nudgeBefore 列出从 1 开始的请求序号，这些请求的提示必须包含注入的纠正轮次。
		// （接上行）
		nudgeBefore []int
	}{
		{
			name:        "normal visible answer does not regress",
			replies:     []llm.Message{textReply("done")},
			wantStopped: StopComplete,
			wantTurns:   1,
		},
		{
			name:            "empty reply is corrected once and then succeeds",
			replies:         []llm.Message{empty, textReply("done")},
			wantStopped:     StopComplete,
			wantTurns:       2,
			wantCorrections: 1,
			nudgeBefore:     []int{2},
		},
		{
			name:            "reasoning only reply is corrected once and then succeeds",
			replies:         []llm.Message{reasoning, textReply("done")},
			wantStopped:     StopComplete,
			wantTurns:       2,
			wantCorrections: 1,
			nudgeBefore:     []int{2},
		},
		{
			name:            "two consecutive empty replies stay within the bound",
			replies:         []llm.Message{empty, empty, textReply("done")},
			wantStopped:     StopComplete,
			wantTurns:       3,
			wantCorrections: 2,
			nudgeBefore:     []int{2, 3},
		},
		{
			name:            "three consecutive empty replies stop at the bound",
			replies:         []llm.Message{empty, empty, empty},
			wantStopped:     StopComplete,
			wantTurns:       3,
			wantCorrections: 2,
			nudgeBefore:     []int{2, 3},
		},
		{
			name:            "a tool batch resets the streak for later empty replies",
			replies:         []llm.Message{empty, empty, toolReply(toolCall("c1", "read", `{}`)), empty, empty, textReply("final")},
			wantStopped:     StopComplete,
			wantTurns:       6,
			wantCorrections: 4,
			wantToolCalls:   1,
			nudgeBefore:     []int{2, 3, 5, 6},
		},
		{
			name:            "a tool batch is never nudged and clears the streak",
			replies:         []llm.Message{empty, toolReply(toolCall("c1", "read", `{}`)), textReply("done")},
			wantStopped:     StopComplete,
			wantTurns:       3,
			wantCorrections: 1,
			wantToolCalls:   1,
			nudgeBefore:     []int{2},
		},
		{
			name:        "tool finish without parts downgrades to a stop",
			replies:     []llm.Message{downgradedToolReply("tool_calls")},
			wantStopped: StopComplete,
			wantTurns:   1,
		},
		{
			name:        "anthropic tool finish without parts downgrades too",
			replies:     []llm.Message{downgradedToolReply("tool_use")},
			wantStopped: StopComplete,
			wantTurns:   1,
		},
		{
			name:        "truncated empty reply keeps the output limit path",
			replies:     []llm.Message{lengthEmpty, textReply("finished")},
			wantStopped: StopComplete,
			wantTurns:   2,
		},
		{
			name:        "truncated tool batch keeps the output limit path",
			replies:     []llm.Message{truncatedTool, textReply("finished")},
			wantStopped: StopComplete,
			wantTurns:   2,
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
			if tools.count() != tt.wantToolCalls {
				t.Fatalf("tool calls = %d, want %d", tools.count(), tt.wantToolCalls)
			}
			if got := countUser(res.Messages, emptyReplyCorrection); got != tt.wantCorrections {
				t.Fatalf("correction turns = %d, want %d: %+v", got, tt.wantCorrections, res.Messages)
			}

			// 每次提示必须落在紧接着的下一次请求中，否则模型看不到纠正，无法恢复。
			// （接上行）
			seen := chat.requestsSeen()
			for _, n := range tt.nudgeBefore {
				if n < 2 || n > len(seen) {
					t.Fatalf("nudge request %d out of range (requests = %d)", n, len(seen))
				}
				if !containsUser(seen[n-1], emptyReplyCorrection) {
					t.Fatalf("request %d did not see the correction prompt", n)
				}
			}

			// 每次纠正都会被宣告；立即停止的运行绝不能发出纠正通知。
			// （接上行）
			notices := 0
			for _, event := range sink.events {
				if event.Kind == EventNotice && strings.Contains(event.Text, "empty reply") {
					notices++
				}
			}
			if notices != tt.wantCorrections {
				t.Fatalf("correction notices = %d, want %d", notices, tt.wantCorrections)
			}
		})
	}
}
