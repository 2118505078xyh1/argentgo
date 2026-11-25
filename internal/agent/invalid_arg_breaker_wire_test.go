package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"example.com/argentgo/internal/llm"
)

// badArgsErr 模仿内置工具拒绝调用参数时使用的文本形状（"<name>: bad arguments: %w"），熔断器正是据此
// 判断工具错误是校验失败而非运行时失败。
// （接上行）
func badArgsErr() error { return errors.New("read: bad arguments: invalid JSON") }

func TestIsInvalidArgError(t *testing.T) {
	tests := []struct {
		name string
		msg  string
		want bool
	}{
		{"malformed JSON marker", "read: bad arguments: unexpected end of JSON input", true},
		{"i18n english", "invalid arguments: missing field", true},
		{"i18n chinese", "参数无效：缺少字段", true},
		{"skill tool", "save_skill: invalid arguments: missing name", true},
		{"unrelated runtime failure", "exit status 1", false},
		{"unknown tool", `unknown tool "bogus"`, false},
		{"denial", "the operator denied this call", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isInvalidArgError(tt.msg); got != tt.want {
				t.Errorf("isInvalidArgError(%q) = %v, want %v", tt.msg, got, tt.want)
			}
		})
	}
}

// TestRunToolsStopsAfterThreeInvalidArgCalls 锁定 P0 行为：一轮中三次参数校验拒绝，会使 runTools 返回一个停止原因，
// 循环可以像其他提前停止一样传播它。
// （接上行）
func TestRunToolsStopsAfterThreeInvalidArgCalls(t *testing.T) {
	ft := &fakeTools{run: func(context.Context, string, string) (string, error) {
		return "", badArgsErr()
	}}
	s := newRunState(Options{Tools: ft}, "go", nil)
	calls := []llm.ToolCall{
		toolCall("c1", "read", `{"n":1}`),
		toolCall("c2", "read", `{"n":2}`),
		toolCall("c3", "read", `{"n":3}`),
	}

	if got := s.runTools(context.Background(), 0, calls); got != stopInvalidArguments {
		t.Fatalf("runTools stop = %q, want %q", got, stopInvalidArguments)
	}
	if got := s.breaker().Count(); got != 3 {
		t.Fatalf("breaker count = %d, want 3", got)
	}
}

// TestRunToolsSuccessClearsBreakerAndRearms 检查连续序列中间的一次成功调用会清除计数，
// 使之后一次无效调用从头重新武装，而不是立即触发阈值。
// （接上行）
func TestRunToolsSuccessClearsBreakerAndRearms(t *testing.T) {
	ft := &fakeTools{run: func(_ context.Context, _ string, args string) (string, error) {
		if strings.Contains(args, `"ok":true`) {
			return "ok", nil
		}
		return "", badArgsErr()
	}}
	s := newRunState(Options{Tools: ft}, "go", nil)
	calls := []llm.ToolCall{
		toolCall("c1", "read", `{"n":1}`),     // 1
		toolCall("c2", "read", `{"n":2}`),     // 2
		toolCall("c3", "read", `{"ok":true}`), // success: 2 -> 0
		toolCall("c4", "read", `{"n":4}`),     // 重新武装：1
	}

	if got := s.runTools(context.Background(), 0, calls); got != "" {
		t.Fatalf("runTools stop = %q, want empty (success must clear)", got)
	}
	if got := s.breaker().Count(); got != 1 {
		t.Fatalf("breaker count = %d, want 1 after clear + re-arm", got)
	}
}

// TestRunToolsResetsBreakerAcrossTurns 验证轮次边界：一个批次留下的连续序列不得带入下一批次的计数。
// （接上行）
func TestRunToolsResetsBreakerAcrossTurns(t *testing.T) {
	ft := &fakeTools{run: func(context.Context, string, string) (string, error) {
		return "", badArgsErr()
	}}
	s := newRunState(Options{Tools: ft}, "go", nil)

	first := []llm.ToolCall{
		toolCall("c1", "read", `{"n":1}`),
		toolCall("c2", "read", `{"n":2}`),
	}
	if got := s.runTools(context.Background(), 0, first); got != "" {
		t.Fatalf("turn 1 stop = %q, want empty", got)
	}
	if got := s.breaker().Count(); got != 2 {
		t.Fatalf("turn 1 breaker count = %d, want 2", got)
	}

	second := []llm.ToolCall{toolCall("c3", "read", `{"n":3}`)}
	if got := s.runTools(context.Background(), 1, second); got != "" {
		t.Fatalf("turn 2 stop = %q, want empty after reset", got)
	}
	if got := s.breaker().Count(); got != 1 {
		t.Fatalf("turn 2 breaker count = %d, want 1 (reset at turn boundary)", got)
	}
}

// TestRunToolsNormalToolsDoNotTripBreaker 防范误报：一批普通的成功调用必须让熔断器保持原样。
// （接上行）
func TestRunToolsNormalToolsDoNotTripBreaker(t *testing.T) {
	ft := &fakeTools{run: func(context.Context, string, string) (string, error) {
		return "ok", nil
	}}
	s := newRunState(Options{Tools: ft}, "go", nil)
	calls := []llm.ToolCall{
		toolCall("c1", "bash", `{"cmd":"echo hi"}`),
		toolCall("c2", "bash", `{"cmd":"echo bye"}`),
		toolCall("c3", "bash", `{"cmd":"true"}`),
	}

	if got := s.runTools(context.Background(), 0, calls); got != "" {
		t.Fatalf("runTools stop = %q, want empty for successful tools", got)
	}
	if got := s.breaker().Count(); got != 0 {
		t.Fatalf("breaker count = %d, want 0", got)
	}
}

// TestRunToolsOtherFailureClearsBreaker 锁定结果语义：与参数校验无关的失败会清除连续序列而非计数。
// （接上行）
func TestRunToolsOtherFailureClearsBreaker(t *testing.T) {
	ft := &fakeTools{run: func(_ context.Context, _ string, args string) (string, error) {
		if strings.Contains(args, `"boom":true`) {
			return "", errors.New("exit status 1")
		}
		return "", badArgsErr()
	}}
	s := newRunState(Options{Tools: ft}, "go", nil)
	calls := []llm.ToolCall{
		toolCall("c1", "bash", `{"n":1}`),
		toolCall("c2", "bash", `{"n":2}`),
		toolCall("c3", "bash", `{"boom":true}`),
	}

	if got := s.runTools(context.Background(), 0, calls); got != "" {
		t.Fatalf("runTools stop = %q, want empty after non-argument failure", got)
	}
	if got := s.breaker().Count(); got != 0 {
		t.Fatalf("breaker count = %d, want 0", got)
	}
}

// TestLoopStopsOnRepeatedInvalidArgs 是端到端检查：来自 runTools 的停止原因通过既有的通用停止路径，同时到达 Result.Stopped 和 EventStop 流，
// 且不引入新的事件种类。
// （接上行）
func TestLoopStopsOnRepeatedInvalidArgs(t *testing.T) {
	chat := &fakeChatter{fn: func(int, []llm.Message, []llm.ToolSpec, func(string)) (llm.Message, error) {
		return toolReply(
			toolCall("c1", "read", `{"n":1}`),
			toolCall("c2", "read", `{"n":2}`),
			toolCall("c3", "read", `{"n":3}`),
		), nil
	}}
	ft := &fakeTools{run: func(context.Context, string, string) (string, error) {
		return "", badArgsErr()
	}}
	s := newRunState(Options{
		Model: "test", Chatter: chat, Tools: ft, MaxTurns: 5,
	}, "go", nil)

	res, err := s.loop(context.Background())
	if err != nil {
		t.Fatalf("loop() error: %v", err)
	}
	if res.Stopped != stopInvalidArguments {
		t.Fatalf("Stopped = %q, want %q", res.Stopped, stopInvalidArguments)
	}
	stopped := false
	for _, e := range s.eventsSnapshot() {
		if e.Kind == EventStop && e.Reason == stopInvalidArguments {
			stopped = true
		}
	}
	if !stopped {
		t.Fatal("no EventStop with the invalid-argument stop reason was emitted")
	}
}
