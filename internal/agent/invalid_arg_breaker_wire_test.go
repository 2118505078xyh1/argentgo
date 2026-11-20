package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// badArgsErr mirrors the text shape built-in tools use when they reject a
// call's arguments ("<name>: bad arguments: %w"), which is how the breaker
// learns that a tool error is a validation failure rather than a runtime one.
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

// TestRunToolsStopsAfterThreeInvalidArgCalls pins the P0 behaviour: three
// argument-validation rejections in one turn make runTools return a stop reason
// that the loop can propagate like any other early stop.
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

// TestRunToolsSuccessClearsBreakerAndRearms checks that a successful call in
// the middle of a streak clears the count, so a later invalid call re-arms from
// scratch instead of immediately tripping the threshold.
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
		toolCall("c4", "read", `{"n":4}`),     // re-arm: 1
	}

	if got := s.runTools(context.Background(), 0, calls); got != "" {
		t.Fatalf("runTools stop = %q, want empty (success must clear)", got)
	}
	if got := s.breaker().Count(); got != 1 {
		t.Fatalf("breaker count = %d, want 1 after clear + re-arm", got)
	}
}

// TestRunToolsResetsBreakerAcrossTurns verifies the turn boundary: a streak
// left by one batch must not carry into the next batch's count.
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

// TestRunToolsNormalToolsDoNotTripBreaker guards against false positives: a
// batch of ordinary successful calls must leave the breaker untouched.
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

// TestRunToolsOtherFailureClearsBreaker pins the outcome semantics: a failure
// unrelated to argument validation clears the streak rather than counting.
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

// TestLoopStopsOnRepeatedInvalidArgs is the end-to-end check: the stop reason
// from runTools reaches both Result.Stopped and the EventStop stream, using the
// existing generic stop path with no new event kind.
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
