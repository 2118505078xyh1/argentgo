package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"example.com/argentgo/internal/llm"
)

// TestVerifySkippedWhenToolBatchDidNotRun 锁定编辑后验证的边界：只有在变更型工具真正执行后验证才有意义。
// 被整体拒绝的批次（参数被截断、BeforeTool 钩子失败）不会改变磁盘，因此它不得消耗反思预算、
// 注入 FAIL 反馈，或以 StopVerifyReflections 结束运行。
// （接上行）
// （接上行）
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

// TestVerifyCannotSpuriousStopAnUneditedRun 证明严重后果：一个持续发出被截断的变更型调用的模型从未真正运行工具，
// 但反思预算不得被消耗，运行也不得作为验证失败而结束。有 bug 时，第四次被截断的回复会以
// （接上行）
// （接上行）
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
