package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// Helpers that script the two tool-free classes the loop must correct, plus a
// malformed tool turn that must be downgraded to a stop instead.
func noContentReply(finish string) llm.Message {
	return llm.Message{Role: "assistant", FinishReason: finish}
}

func reasoningOnlyReply(text string) llm.Message {
	return llm.Message{Role: "assistant", Reasoning: text, FinishReason: "stop"}
}

func downgradedToolReply(finish string) llm.Message {
	return llm.Message{Role: "assistant", FinishReason: finish}
}

// TestEmptyReplyCorrectionWiring pins the loop-level behaviour of the response
// four-classification: empty and reasoning-only replies get a bounded chance
// to answer, while a normal answer, a tool batch and a downgraded tool turn
// keep their existing stop semantics.
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
		// nudgeBefore lists the 1-based request numbers whose prompt must
		// contain the injected correction turn.
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

			// Each nudge must land in the very next request, otherwise the
			// model never sees the correction and cannot recover.
			seen := chat.requestsSeen()
			for _, n := range tt.nudgeBefore {
				if n < 2 || n > len(seen) {
					t.Fatalf("nudge request %d out of range (requests = %d)", n, len(seen))
				}
				if !containsUser(seen[n-1], emptyReplyCorrection) {
					t.Fatalf("request %d did not see the correction prompt", n)
				}
			}

			// Every correction is announced; runs that stop immediately must
			// not emit a correction notice at all.
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
