package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// TestInvokeTracksToolFailuresAndReset 检查失败的工具执行会被记录为错误，而成功的执行会清除连续序列。
// （接上行）
func TestInvokeTracksToolFailuresAndReset(t *testing.T) {
	ft := &fakeTools{}
	s := &runState{
		tools:    ft,
		mistakes: NewMistakeTracker(0, defaultMistakeGuidance),
	}
	call := toolCall("c1", "bad", "{}")

	ft.run = func(context.Context, string, string) (string, error) {
		return "", errors.New("boom")
	}
	res := s.invoke(context.Background(), call)
	if !res.isError {
		t.Fatalf("first invoke isError = false, want true")
	}
	if got := s.mistakes.Count(); got != 1 {
		t.Fatalf("mistakes.Count() after failure = %d, want 1", got)
	}
	if got := s.mistakes.CountFor(MistakeKindToolExecutionFailed); got != 1 {
		t.Fatalf("CountFor(tool_execution_failed) = %d, want 1", got)
	}

	ft.run = nil // 成功路径
	res = s.invoke(context.Background(), call)
	if res.isError {
		t.Fatalf("second invoke isError = true, want false")
	}
	if got := s.mistakes.Count(); got != 0 {
		t.Fatalf("mistakes.Count() after success = %d, want 0", got)
	}
}

// TestLoopInjectsMistakeGuidance 检查连续序列达到上限后，恢复指令会在下一次请求之前作为用户轮次注入。
// （接上行）
func TestLoopInjectsMistakeGuidance(t *testing.T) {
	chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n == 1 {
			return toolReply(toolCall("c1", "bad", "{}")), nil
		}
		return textReply("done"), nil
	}}
	ft := &fakeTools{run: func(context.Context, string, string) (string, error) {
		return "", errors.New("boom")
	}}
	s := newRunState(Options{
		Model:    "test",
		Chatter:  chat,
		Tools:    ft,
		MaxTurns: 5,
	}, "go", nil)
	s.mistakes = NewMistakeTracker(1, func(MistakeKind, int) string { return "RECOVERY" })

	res, err := s.loop(context.Background())
	if err != nil {
		t.Fatalf("loop() error: %v", err)
	}
	if res.Stopped != StopComplete {
		t.Fatalf("Stopped = %q, want %q", res.Stopped, StopComplete)
	}
	seen := chat.requestsSeen()
	if len(seen) < 2 {
		t.Fatalf("provider saw %d requests, want >= 2", len(seen))
	}
	found := false
	for _, m := range seen[1] {
		if m.Role == "user" && m.Content == "RECOVERY" {
			found = true
		}
	}
	if !found {
		t.Fatalf("second request lacks the injected guidance: %+v", seen[1])
	}
}

// TestLoopStopsOnUnrecoverableMistakes 检查没有指引回调的连续序列会通过既有的 EventStop/结果终止路径结束运行，
// 且在发出任何模型请求之前结束。
// （接上行）
func TestLoopStopsOnUnrecoverableMistakes(t *testing.T) {
	chat := &fakeChatter{fn: func(int, []llm.Message, []llm.ToolSpec, func(string)) (llm.Message, error) {
		return textReply("should not be called"), nil
	}}
	s := newRunState(Options{
		Model:   "test",
		Chatter: chat,
	}, "go", nil)
	s.mistakes = NewMistakeTracker(1) // 无指引：Check 必须停止
	s.mistakes.Record(MistakeKindAPIError)

	res, err := s.loop(context.Background())
	if err != nil {
		t.Fatalf("loop() error: %v", err)
	}
	if res.Stopped != stopRepeatedMistakes {
		t.Fatalf("Stopped = %q, want %q", res.Stopped, stopRepeatedMistakes)
	}
	if got := chat.count(); got != 0 {
		t.Fatalf("provider saw %d requests, want 0 (stopped before any call)", got)
	}
	// 停止原因也必须到达事件流。
	stopped := false
	for _, e := range s.eventsSnapshot() {
		if e.Kind == EventStop && e.Reason == stopRepeatedMistakes {
			stopped = true
		}
	}
	if !stopped {
		t.Fatal("no EventStop with the mistake stop reason was emitted")
	}
}
