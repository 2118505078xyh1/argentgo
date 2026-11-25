package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"example.com/argentgo/internal/llm"
	"example.com/argentgo/internal/security"
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

// TestRecoverablePlanBlockContinuesTheRun 驱动一次完整的派发：工具返回防护器触发的哨兵，循环必须写入正常的工具结果
// 并让模型再走一轮（StopComplete），绝不走终结性的 StopError 路径。分类辅助函数已在上面单独演练；
// 本测试锁定它所守护的接缝的端到端行为。
// （接上行）
// （接上行）
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
