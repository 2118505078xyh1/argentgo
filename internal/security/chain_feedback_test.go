package security

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

// feedbackApprover is an Approver that can answer with an operator correction.
// It exercises the new channel through the same Ask(bool, error) API every
// existing approver uses.
type feedbackApprover struct {
	allow    bool
	feedback string
	err      error
	calls    int
}

func (a *feedbackApprover) Ask(ctx context.Context, _ string, _ string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	a.calls++
	if a.err != nil {
		return false, a.err
	}
	if a.allow {
		return true, nil
	}
	if a.feedback != "" {
		return false, &FeedbackRefusal{Feedback: a.feedback}
	}
	return false, nil
}

func feedbackTestChain(t *testing.T, approver Approver) *Chain {
	t.Helper()
	dir := t.TempDir()
	return NewChain(DefaultPolicy(dir), approver, NewAuditLog(dir), "s1")
}

func TestChainRefusalWithFeedbackBecomesModelReadableError(t *testing.T) {
	approver := &feedbackApprover{feedback: "改成这样再执行：写 docs/README.md"}
	chain := feedbackTestChain(t, approver)
	err := chain.Check(context.Background(), "write", json.RawMessage(`{"path":"a.txt"}`))

	var refusal *RefusalError
	if !errors.As(err, &refusal) {
		t.Fatalf("got %v, want *RefusalError", err)
	}
	if refusal.Tool != "write" {
		t.Fatalf("tool = %q, want write", refusal.Tool)
	}
	if !strings.Contains(refusal.Feedback, "改成这样再执行") {
		t.Fatalf("feedback = %q, want the operator's correction", refusal.Feedback)
	}
	if !errors.Is(err, ErrRefused) || errors.Is(err, ErrDenied) {
		t.Fatalf("got %v, want ErrRefused only", err)
	}
	var stop *StopError
	if errors.As(err, &stop) {
		t.Fatalf("a correction must not be a final StopError: %#v", err)
	}
	for _, want := range []string{"改成这样再执行", "Adjust the request and try again"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("tool error %q does not contain %q", err.Error(), want)
		}
	}
	if strings.Contains(err.Error(), "intentional and final") {
		t.Fatalf("tool error must not tell the model the block is final: %q", err.Error())
	}

	entries, readErr := ReadAudit(chain.Audit.Path())
	if readErr != nil || len(entries) != 1 {
		t.Fatalf("audit: %v entries=%d", readErr, len(entries))
	}
	if entries[0].Decision != "refused" {
		t.Fatalf("audit entry = %+v", entries[0])
	}
	if strings.Contains(entries[0].Reason, "改成这样") {
		t.Fatalf("audit reason leaked operator feedback: %q", entries[0].Reason)
	}
}

func TestChainPlainRefusalKeepsFinalStopError(t *testing.T) {
	for _, tc := range []struct {
		name     string
		approver Approver
	}{
		{"bool only", &feedbackApprover{}},
		{"empty feedback", &feedbackApprover{feedback: "   "}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chain := feedbackTestChain(t, tc.approver)
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
				t.Fatalf("a refusal without feedback must not be a RefusalError")
			}
		})
	}
}

func TestChainFeedbackCapableApproverStillApproves(t *testing.T) {
	approver := &feedbackApprover{allow: true, feedback: "ignored when allowed"}
	chain := feedbackTestChain(t, approver)
	if err := chain.Check(context.Background(), "write", json.RawMessage(`{"path":"a.txt"}`)); err != nil {
		t.Fatalf("approved call: %v", err)
	}
	entries, readErr := ReadAudit(chain.Audit.Path())
	if readErr != nil || len(entries) != 1 || entries[0].Decision != "allow" {
		t.Fatalf("audit: %v entries=%+v", readErr, entries)
	}
}

func TestChainFeedbackRefusalSurvivesApproverWrappers(t *testing.T) {
	for _, tc := range []struct {
		name string
		wrap func(Approver) Approver
	}{
		{"cached", func(a Approver) Approver { return NewCachedApprover(a) }},
		{"scoped", func(a Approver) Approver { return NewScopedApprover(a) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inner := &feedbackApprover{feedback: "use docs instead"}
			chain := feedbackTestChain(t, tc.wrap(inner))
			for i := 0; i < 2; i++ {
				err := chain.Check(context.Background(), "write", json.RawMessage(`{"path":"a.txt"}`))
				var refusal *RefusalError
				if !errors.As(err, &refusal) || refusal.Feedback != "use docs instead" {
					t.Fatalf("call %d: got %v, want the feedback refusal", i, err)
				}
			}
			if inner.calls != 2 {
				t.Fatalf("asked %d times, want 2 (refusals are never cached)", inner.calls)
			}
		})
	}
}

func TestChainFeedbackRefusalClipsLongTextOnRuneBoundary(t *testing.T) {
	approver := &feedbackApprover{feedback: strings.Repeat("改", maxApprovalFeedbackRunes+50)}
	chain := feedbackTestChain(t, approver)
	err := chain.Check(context.Background(), "write", json.RawMessage(`{"path":"a.txt"}`))
	var refusal *RefusalError
	if !errors.As(err, &refusal) {
		t.Fatalf("got %v, want *RefusalError", err)
	}
	if !utf8.ValidString(refusal.Feedback) {
		t.Fatal("clipped feedback is not valid UTF-8")
	}
	if !strings.HasSuffix(refusal.Feedback, "…[feedback clipped]") {
		t.Fatalf("clipped feedback lacks the truncation marker: %q", refusal.Feedback)
	}
	if got := utf8.RuneCountInString(refusal.Feedback); got > maxApprovalFeedbackRunes+len("…[feedback clipped]") {
		t.Fatalf("feedback has %d runes, want at most %d", got, maxApprovalFeedbackRunes+len("…[feedback clipped]"))
	}
}

func TestChainApproverErrorIsNotAFeedbackRefusal(t *testing.T) {
	approver := &feedbackApprover{err: errors.New("stdin broken")}
	chain := feedbackTestChain(t, approver)
	err := chain.Check(context.Background(), "write", json.RawMessage(`{"path":"a.txt"}`))
	if err == nil || !strings.Contains(err.Error(), "approval for the write tool call failed") {
		t.Fatalf("got %v, want the wrapped approval failure", err)
	}
	var refusal *RefusalError
	if errors.As(err, &refusal) {
		t.Fatalf("an infrastructure error must not become a feedback refusal: %v", err)
	}
}
