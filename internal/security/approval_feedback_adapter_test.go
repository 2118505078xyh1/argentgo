package security

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// adapterResultApprover 把固定的 (granted, err) 原样返回，用来把适配原语产出的
// 错误喂进真实的 Chain，验证端到端翻译结果。
type adapterResultApprover struct {
	granted bool
	err     error
}

func (a *adapterResultApprover) Ask(context.Context, string, string) (bool, error) {
	return a.granted, a.err
}

func TestAnswerToError(t *testing.T) {
	const cjk = "改成这样再执行：写 docs/README.md，别碰 a.txt"
	tests := []struct {
		name         string
		answer       FeedbackAnswer
		wantNil      bool
		wantSentinel error  // errors.Is 应匹配的哨兵
		wantRefusal  bool   // errors.As 应匹配 *FeedbackRefusal
		wantFeedback string // 期望携带的反馈文本
	}{
		{"granted without feedback", FeedbackAnswer{Granted: true}, true, nil, false, ""},
		{"granted with feedback is ignored", FeedbackAnswer{Granted: true, Feedback: "ignore me"}, true, nil, false, ""},
		{"denied with feedback", FeedbackAnswer{Feedback: "use docs instead"}, false, nil, true, "use docs instead"},
		{"denied with CJK feedback", FeedbackAnswer{Feedback: cjk}, false, nil, true, cjk},
		{"denied with empty feedback", FeedbackAnswer{}, false, ErrRefused, false, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := AnswerToError(tc.answer)
			if tc.wantNil {
				if err != nil {
					t.Fatalf("granted answer returned %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("denied answer returned nil, want an error")
			}
			if tc.wantSentinel != nil && !errors.Is(err, tc.wantSentinel) {
				t.Fatalf("got %v, want errors.Is(err, %v)", err, tc.wantSentinel)
			}
			if tc.wantRefusal {
				var refusal *FeedbackRefusal
				if !errors.As(err, &refusal) {
					t.Fatalf("got %v, want *FeedbackRefusal", err)
				}
				if refusal.Feedback != tc.wantFeedback {
					t.Fatalf("feedback = %q, want %q", refusal.Feedback, tc.wantFeedback)
				}
				if !strings.Contains(err.Error(), tc.wantFeedback) {
					t.Fatalf("error %q does not carry the feedback", err.Error())
				}
				// 反馈拒绝不携带终结哨兵：它应作为可恢复的工具错误到达模型。
				if errors.Is(err, ErrRefused) || errors.Is(err, ErrDenied) {
					t.Fatalf("feedback refusal must not match a terminal sentinel: %v", err)
				}
			} else {
				var refusal *FeedbackRefusal
				if errors.As(err, &refusal) {
					t.Fatalf("plain denial must not be a *FeedbackRefusal: %v", err)
				}
				if err != ErrRefused {
					t.Fatalf("got %v, want the bare ErrRefused sentinel", err)
				}
				if errors.Is(err, ErrDenied) {
					t.Fatalf("operator refusal must not match ErrDenied: %v", err)
				}
			}
		})
	}
}

func TestAnswerToErrorErrorSemantics(t *testing.T) {
	// 带反馈的拒绝：errors.As 命中 FeedbackRefusal；FeedbackRefusal 没有 Unwrap，
	// 因此不匹配 ErrRefused / ErrDenied，链才会把它当作可恢复错误而非终结拒绝。
	feedbackErr := AnswerToError(FeedbackAnswer{Feedback: "修正后再提交"})
	var refusal *FeedbackRefusal
	if !errors.As(feedbackErr, &refusal) {
		t.Fatalf("got %v, want *FeedbackRefusal", feedbackErr)
	}
	if errors.Is(feedbackErr, ErrRefused) || errors.Is(feedbackErr, ErrDenied) {
		t.Fatalf("feedback refusal must not match a sentinel: %v", feedbackErr)
	}

	// 空反馈拒绝：就是 ErrRefused 哨兵本身，errors.Is 命中、errors.As 不命中。
	plain := AnswerToError(FeedbackAnswer{})
	if !errors.Is(plain, ErrRefused) {
		t.Fatalf("plain denial must match ErrRefused: %v", plain)
	}
	if errors.As(plain, &refusal) {
		t.Fatalf("plain denial must not be a *FeedbackRefusal: %v", plain)
	}
}

func TestParseApproverResult(t *testing.T) {
	inner := &FeedbackRefusal{Feedback: "请改写 config"}
	wrapped := fmt.Errorf("approval: %w", inner)
	infraErr := errors.New("stdin broken")

	tests := []struct {
		name    string
		granted bool
		err     error
		want    FeedbackAnswer
	}{
		{"plain grant", true, nil, FeedbackAnswer{Granted: true}},
		{"plain denial", false, nil, FeedbackAnswer{}},
		{"feedback refusal", false, inner, FeedbackAnswer{Feedback: "请改写 config"}},
		{"wrapped feedback refusal", false, wrapped, FeedbackAnswer{Feedback: "请改写 config"}},
		{"feedback refusal overrides granted", true, inner, FeedbackAnswer{Feedback: "请改写 config"}},
		{"unrelated error keeps grant", true, infraErr, FeedbackAnswer{Granted: true}},
		{"unrelated error keeps denial", false, infraErr, FeedbackAnswer{}},
		{"cjk feedback refusal", false, &FeedbackRefusal{Feedback: "改成中文再提交"}, FeedbackAnswer{Feedback: "改成中文再提交"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseApproverResult(tc.granted, tc.err)
			if got.Granted != tc.want.Granted || got.Feedback != tc.want.Feedback {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

// 端到端：Parse → AnswerToError 产出的反馈拒绝喂进 Chain 后应变成 RefusalError，
// 而不是终结性的 StopError。（ErrRefused 作为哨兵的终结语义由上层错误分类消费，
// 不作为 Approver.Ask 的错误直接回灌，故这里只验证反馈路径。）
func TestAnswerToErrorFeedbackBecomesRefusalErrorInChain(t *testing.T) {
	const feedback = "改成这样：写 docs/README.md"
	answer := ParseApproverResult(false, &FeedbackRefusal{Feedback: feedback})
	err := AnswerToError(answer)

	dir := t.TempDir()
	chain := NewChain(DefaultPolicy(dir), &adapterResultApprover{granted: answer.Granted, err: err}, NewAuditLog(dir), "s1")
	got := chain.Check(context.Background(), "write", json.RawMessage(`{"path":"a.txt"}`))

	var refusal *RefusalError
	if !errors.As(got, &refusal) {
		t.Fatalf("got %v, want *RefusalError", got)
	}
	if refusal.Feedback != feedback {
		t.Fatalf("feedback = %q, want %q", refusal.Feedback, feedback)
	}
	if !errors.Is(got, ErrRefused) || errors.Is(got, ErrDenied) {
		t.Fatalf("refusal must match ErrRefused only: %v", got)
	}
	var stop *StopError
	if errors.As(got, &stop) {
		t.Fatalf("feedback refusal must not become a final StopError: %v", got)
	}
}
