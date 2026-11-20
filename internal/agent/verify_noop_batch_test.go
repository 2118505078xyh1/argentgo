package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// TestVerifySkippedWhenToolBatchDidNotRun pins the post-edit verification
// boundary: verification is meaningful only after a mutating tool actually
// executed. A batch rejected wholesale (truncated arguments, a failing
// BeforeTool hook) changes nothing on disk, so it must not spend the reflection
// budget, inject FAIL feedback, or end the run with StopVerifyReflections.
func TestVerifySkippedWhenToolBatchDidNotRun(t *testing.T) {
	cases := []struct {
		name          string
		calls         []llm.ToolCall
		finishReason  string
		beforeToolErr error
		wantVerify    int
		wantFeedback  bool
	}{
		{
			name:         "dispatched mutating batch verifies",
			wantVerify:   1,
			wantFeedback: true,
		},
		{
			name:         "truncated mutating batch skips verification",
			finishReason: "length",
		},
		{
			name:          "beforeTool failure skips verification",
			beforeToolErr: errors.New("session not ready"),
		},
		{
			name: "invalid call IDs skip verification",
			calls: []llm.ToolCall{
				toolCall("c1", "write", `{}`), toolCall("c1", "write", `{}`),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := tc.calls
			if len(calls) == 0 {
				calls = []llm.ToolCall{toolCall("c1", "write", `{"i":"create"}`)}
			}
			chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
				if n == 1 {
					reply := toolReply(calls...)
					reply.FinishReason = tc.finishReason
					return reply, nil
				}
				return textReply("done"), nil
			}}
			verifyCalls := 0
			opts := Options{
				Model:                "test",
				Chatter:              chat,
				Tools:                &fakeTools{},
				MaxTurns:             6,
				MaxVerifyReflections: 1,
				BeforeTool:           func() error { return tc.beforeToolErr },
				Verify: func(context.Context, []string) (string, bool, string, bool) {
					verifyCalls++
					return "go test", false, "FAIL: assertion x", true
				},
			}
			s := newRunState(opts, "write it", nil)
			res, err := s.loop(context.Background())
			if err != nil {
				t.Fatalf("loop() error: %v", err)
			}
			if res.Stopped != StopComplete {
				t.Fatalf("Stopped = %q, want %q", res.Stopped, StopComplete)
			}
			if verifyCalls != tc.wantVerify {
				t.Fatalf("Verify called %d times, want %d", verifyCalls, tc.wantVerify)
			}
			if got := s.verifyState.Reflections; got != tc.wantVerify {
				t.Fatalf("Reflections = %d, want %d", got, tc.wantVerify)
			}
			feedback := false
			for _, m := range res.Messages {
				if m.Role == "user" && strings.Contains(m.Content, "[verify: GO TEST] FAIL") {
					feedback = true
				}
			}
			if feedback != tc.wantFeedback {
				t.Fatalf("verify feedback in transcript = %v, want %v", feedback, tc.wantFeedback)
			}
		})
	}
}

// TestVerifyCannotSpuriousStopAnUneditedRun proves the severe consequence: a
// model that keeps emitting truncated mutating calls never runs a tool, yet the
// reflection budget must not be spent and the run must not end as a verify
// failure. With the bug present the fourth truncated reply ends the run with
// StopVerifyReflections.
func TestVerifyCannotSpuriousStopAnUneditedRun(t *testing.T) {
	chat := &fakeChatter{fn: func(int, []llm.Message, []llm.ToolSpec, func(string)) (llm.Message, error) {
		reply := toolReply(toolCall("c1", "write", `{"i":"create"}`))
		reply.FinishReason = "length"
		return reply, nil
	}}
	verifyCalls := 0
	opts := Options{
		Model:    "test",
		Chatter:  chat,
		Tools:    &fakeTools{},
		MaxTurns: 6,
		Verify: func(context.Context, []string) (string, bool, string, bool) {
			verifyCalls++
			return "go test", false, "FAIL: assertion x", true
		},
	}
	s := newRunState(opts, "write it", nil)
	res, err := s.loop(context.Background())
	if err != nil {
		t.Fatalf("loop() error: %v", err)
	}
	if res.Stopped != StopMaxTurns {
		t.Fatalf("Stopped = %q, want %q (no edit happened)", res.Stopped, StopMaxTurns)
	}
	if verifyCalls != 0 {
		t.Fatalf("Verify called %d times on a batch where no tool ran, want 0", verifyCalls)
	}
}
