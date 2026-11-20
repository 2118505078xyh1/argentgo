package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/security"
)

func TestIsRecoverablePlanBlock(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"unrelated error", errors.New("boom"), false},
		{"stop error is not recoverable", &security.StopError{Tool: "bash", Reason: "denied"}, false},
		{"sentinel", security.ErrRecoverablePlanBlock, true},
		{"wrapped sentinel", fmt.Errorf("tool %q rejected: %w", "bash", security.ErrRecoverablePlanBlock), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRecoverablePlanBlock(tc.err); got != tc.want {
				t.Fatalf("isRecoverablePlanBlock(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestRecoverablePlanBlockContinuesTheRun drives a full dispatch: the tool
// returns a guard-triggered sentinel, and the loop must write a normal tool
// result and let the model take a second turn (StopComplete), never a terminal
// StopError path. The classification helper is exercised separately above;
// this test pins the end-to-end behaviour of the seam it guards.
func TestRecoverablePlanBlockContinuesTheRun(t *testing.T) {
	tools := &fakeTools{specs: []llm.ToolSpec{toolSpec("bash")}}
	tools.run = func(_ context.Context, name, _ string) (string, error) {
		return "", fmt.Errorf("tool %q rejected: %w", name, security.ErrRecoverablePlanBlock)
	}
	chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n == 1 {
			return toolReply(toolCall("c1", "bash", `{"command":"rm -rf x"}`)), nil
		}
		return textReply("done"), nil
	}}
	runner := &Runner{Opts: Options{
		Model:    "m",
		Chatter:  chat,
		Tools:    tools,
		MaxTurns: 4,
	}}

	res, err := runner.Run(context.Background(), "investigate")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Stopped != StopComplete {
		t.Fatalf("stopped = %q, want %q (a recoverable block must not stop the run)", res.Stopped, StopComplete)
	}
	if chat.count() != 2 {
		t.Fatalf("model requests = %d, want 2 (the run must continue)", chat.count())
	}
	var toolContent string
	for _, msg := range res.Messages {
		if msg.Role == "tool" {
			toolContent = msg.Content
		}
	}
	if !strings.Contains(toolContent, "blocked in plan mode") {
		t.Fatalf("tool result %q must carry the recoverable guard message", toolContent)
	}
	if !strings.Contains(toolContent, "Continue with a read-only command") {
		t.Fatalf("tool result %q must carry recoverable guidance", toolContent)
	}
}
