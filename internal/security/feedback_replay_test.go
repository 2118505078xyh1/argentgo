package security

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFeedbackReplayApproverAskAlwaysRefusesWithFeedback(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name     string
		feedback string
		ctx      context.Context
		tool     string
		args     string
	}{
		{"ascii feedback", "use docs instead", context.Background(), "write", `{"path":"a.txt"}`},
		{"cjk feedback", "改成这样再执行：写 docs/README.md", context.Background(), "shell", `{"command":"rm -f a.txt"}`},
		{"empty feedback", "", context.Background(), "write", ""},
		{"whitespace feedback", "   ", context.Background(), "patch", "{}"},
		{"cancelled context still refuses", "stop", cancelled, "write", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			approver := NewFeedbackReplayApprover(tc.feedback)
			ok, err := approver.Ask(tc.ctx, tc.tool, tc.args)
			if ok {
				t.Fatal("Ask must never grant")
			}
			var refusal *FeedbackRefusal
			if !errors.As(err, &refusal) {
				t.Fatalf("got %v, want *FeedbackRefusal", err)
			}
			if refusal.Feedback != tc.feedback {
				t.Fatalf("feedback = %q, want %q", refusal.Feedback, tc.feedback)
			}
		})
	}
}

// 端到端：重放反馈审批器接入 Chain 后，应沿 chain.go 的反馈转换产出可恢复的
// RefusalError，而不是终结性的 StopError。
func TestFeedbackReplayApproverInChainProducesRecoverableRefusalError(t *testing.T) {
	tests := []struct {
		name     string
		feedback string
	}{
		{"ascii", "write to docs instead"},
		{"cjk", "改成这样再执行：写 docs/README.md，别碰 a.txt"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			chain := feedbackTestChain(t, NewFeedbackReplayApprover(tc.feedback))
			err := chain.Check(context.Background(), "write", json.RawMessage(`{"path":"a.txt"}`))

			var refusal *RefusalError
			if !errors.As(err, &refusal) {
				t.Fatalf("got %v, want *RefusalError", err)
			}
			if refusal.Tool != "write" {
				t.Fatalf("tool = %q, want write", refusal.Tool)
			}
			if refusal.Feedback != tc.feedback {
				t.Fatalf("feedback = %q, want %q", refusal.Feedback, tc.feedback)
			}
			if !errors.Is(err, ErrRefused) || errors.Is(err, ErrDenied) {
				t.Fatalf("refusal must match ErrRefused only: %v", err)
			}
			var stop *StopError
			if errors.As(err, &stop) {
				t.Fatalf("feedback must not become a final StopError: %v", err)
			}
			for _, want := range []string{tc.feedback, "Adjust the request and try again."} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("tool error %q does not contain %q", err.Error(), want)
				}
			}
			if strings.Contains(err.Error(), "intentional and final") {
				t.Fatalf("tool error must not tell the model the block is final: %q", err.Error())
			}
		})
	}
}

// 空的更正意见不是反馈：链会把它当作单纯的"否"，保留终结性的 StopError。
func TestFeedbackReplayApproverEmptyFeedbackBecomesPlainRefusal(t *testing.T) {
	tests := []struct {
		name     string
		feedback string
	}{
		{"empty", ""},
		{"whitespace only", "   \t "},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			chain := feedbackTestChain(t, NewFeedbackReplayApprover(tc.feedback))
			err := chain.Check(context.Background(), "write", json.RawMessage(`{"path":"a.txt"}`))

			var stop *StopError
			if !errors.As(err, &stop) || !stop.Refused {
				t.Fatalf("got %v, want a refusing StopError", err)
			}
			if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "intentional and final") {
				t.Fatalf("plain refusal changed: %v", err)
			}
			var refusal *RefusalError
			if errors.As(err, &refusal) {
				t.Fatalf("empty feedback must not become a recoverable refusal: %v", err)
			}
		})
	}
}

// 端到端：过长的中文反馈经 Chain.Check 后必须按 rune 裁剪，不能截断 UTF-8。
func TestFeedbackReplayApproverClipsLongFeedbackOnRuneBoundary(t *testing.T) {
	feedback := strings.Repeat("改", maxApprovalFeedbackRunes+50)
	chain := feedbackTestChain(t, NewFeedbackReplayApprover(feedback))
	err := chain.Check(context.Background(), "write", json.RawMessage(`{"path":"a.txt"}`))

	var refusal *RefusalError
	if !errors.As(err, &refusal) {
		t.Fatalf("got %v, want *RefusalError", err)
	}
	if !utf8.ValidString(refusal.Feedback) {
		t.Fatal("clipped feedback is not valid UTF-8")
	}
	const marker = "…[feedback clipped]"
	if !strings.HasSuffix(refusal.Feedback, marker) {
		t.Fatalf("clipped feedback lacks the truncation marker: %q", refusal.Feedback)
	}
	body := strings.TrimSuffix(refusal.Feedback, marker)
	if got := utf8.RuneCountInString(body); got != maxApprovalFeedbackRunes {
		t.Fatalf("clipped body has %d runes, want %d", got, maxApprovalFeedbackRunes)
	}
	if strings.Trim(body, "改") != "" {
		t.Fatalf("clipped body contains unexpected characters: %q", body)
	}
}

func TestRefusalFromFeedback(t *testing.T) {
	tests := []struct {
		name         string
		feedback     string
		wantBare     bool
		wantFeedback string
	}{
		{"empty feedback is plain refusal", "", true, ""},
		{"ascii feedback", "use docs instead", false, "use docs instead"},
		{"cjk feedback", "改成中文再提交", false, "改成中文再提交"},
		{"whitespace feedback is not empty", "   ", false, "   "},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := RefusalFromFeedback(tc.feedback)
			if got == nil {
				t.Fatal("RefusalFromFeedback returned nil")
			}
			// 与 AnswerToError 的便捷包装语义完全等价。
			want := AnswerToError(FeedbackAnswer{Granted: false, Feedback: tc.feedback})
			if got.Error() != want.Error() {
				t.Fatalf("got %q, want AnswerToError equivalence %q", got.Error(), want.Error())
			}

			if tc.wantBare {
				if got != ErrRefused || !errors.Is(got, ErrRefused) {
					t.Fatalf("got %v, want the bare ErrRefused sentinel", got)
				}
				var refusal *FeedbackRefusal
				if errors.As(got, &refusal) {
					t.Fatalf("empty feedback must not be a *FeedbackRefusal: %v", got)
				}
				return
			}

			var refusal *FeedbackRefusal
			if !errors.As(got, &refusal) {
				t.Fatalf("got %v, want *FeedbackRefusal", got)
			}
			if refusal.Feedback != tc.wantFeedback {
				t.Fatalf("feedback = %q, want %q", refusal.Feedback, tc.wantFeedback)
			}
			if !strings.Contains(got.Error(), tc.wantFeedback) {
				t.Fatalf("error %q does not carry the feedback", got.Error())
			}
			if errors.Is(got, ErrRefused) || errors.Is(got, ErrDenied) {
				t.Fatalf("feedback refusal must not match a terminal sentinel: %v", got)
			}
		})
	}
}

// 便捷函数产出的错误同样可以经 Chain 转成可恢复的 RefusalError（接线场景）。
func TestRefusalFromFeedbackBecomesRecoverableRefusalErrorInChain(t *testing.T) {
	const feedback = "请改写 config"
	chain := feedbackTestChain(t, &adapterResultApprover{err: RefusalFromFeedback(feedback)})
	err := chain.Check(context.Background(), "write", json.RawMessage(`{"path":"a.txt"}`))

	var refusal *RefusalError
	if !errors.As(err, &refusal) {
		t.Fatalf("got %v, want *RefusalError", err)
	}
	if refusal.Feedback != feedback || !errors.Is(err, ErrRefused) {
		t.Fatalf("got %v, want a recoverable refusal carrying %q", err, feedback)
	}
}
