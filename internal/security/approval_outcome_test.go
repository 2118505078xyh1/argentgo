package security

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestClassifyApprovalPlainAnswers(t *testing.T) {
	tests := []struct {
		name    string
		granted bool
		want    ApprovalOutcome
	}{
		{"true with no error is allowed", true, ApprovalAllowed},
		{"false with no error is rejected", false, ApprovalRejected},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyApproval(tt.granted, nil); got != tt.want {
				t.Fatalf("ClassifyApproval(%t, nil) = %q, want %q", tt.granted, got, tt.want)
			}
		})
	}
}

func TestClassifyApprovalContextErrorsAreCancelled(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		granted bool
	}{
		{"canceled", context.Canceled, false},
		{"deadline exceeded", context.DeadlineExceeded, false},
		{"wrapped canceled", fmt.Errorf("prompt: %w", context.Canceled), false},
		{"wrapped deadline", fmt.Errorf("prompt: %w", context.DeadlineExceeded), false},
		{"double wrapped canceled", fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", context.Canceled)), true},
		{"joined canceled", errors.Join(errors.New("side"), context.Canceled), true},
		// 带有 context 错误的 granted 标志仍必须被视为已取消，
		// 绝不能被提升为 allow。
		{"granted but canceled", context.Canceled, true},
		{"granted but deadline exceeded", context.DeadlineExceeded, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyApproval(tt.granted, tt.err); got != ApprovalCancelled {
				t.Fatalf("ClassifyApproval(%t, %v) = %q, want %q", tt.granted, tt.err, got, ApprovalCancelled)
			}
		})
	}
}

func TestClassifyApprovalOtherErrorsAreUnavailable(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		granted bool
	}{
		{"plain error", errors.New("stdin broken"), false},
		{"wrapped error", fmt.Errorf("reading answer: %w", errors.New("stdin broken")), false},
		{"granted with plain error", errors.New("channel gone"), true},
		{"granted with wrapped error", fmt.Errorf("channel: %w", errors.New("gone")), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyApproval(tt.granted, tt.err); got != ApprovalUnavailable {
				t.Fatalf("ClassifyApproval(%t, %v) = %q, want %q", tt.granted, tt.err, got, ApprovalUnavailable)
			}
		})
	}
}

func TestClassifyApprovalNeverFailsOpen(t *testing.T) {
	// 穷举每一种有意义的 (granted, err) 组合，并断言
	// 失败即拒绝的不变量：无论 bool 如何，错误都绝不代表同意。
	errorsToTry := []error{
		nil,
		errors.New("unknown"),
		fmt.Errorf("wrapped: %w", errors.New("unknown")),
		context.Canceled,
		context.DeadlineExceeded,
		fmt.Errorf("wrapped: %w", context.Canceled),
	}
	for _, granted := range []bool{false, true} {
		for _, err := range errorsToTry {
			outcome := ClassifyApproval(granted, err)
			if err != nil && outcome == ApprovalAllowed {
				t.Fatalf("ClassifyApproval(%t, %v) failed open: got %q", granted, err, outcome)
			}
			if err == nil && granted && outcome != ApprovalAllowed {
				t.Fatalf("ClassifyApproval(%t, nil) = %q, want %q", granted, outcome, ApprovalAllowed)
			}
		}
	}
}

func TestApprovalOutcomeAllows(t *testing.T) {
	tests := []struct {
		outcome ApprovalOutcome
		want    bool
	}{
		{ApprovalAllowed, true},
		{ApprovalRejected, false},
		{ApprovalUnavailable, false},
		{ApprovalCancelled, false},
		{ApprovalOutcome("bogus"), false}, // 未知值失败即拒绝
	}
	for _, tt := range tests {
		if got := tt.outcome.Allows(); got != tt.want {
			t.Fatalf("%q.Allows() = %t, want %t", tt.outcome, got, tt.want)
		}
	}
}

// machineApprover 模拟一个无头/子 agent 的审批路径，其通道
// 缺失：它给出肯定回答，但也报告通道错误。链
// 必须失败即拒绝，而不是把 true 当作同意。
type machineApprover struct {
	granted bool
	err     error
}

func (m machineApprover) Ask(context.Context, string, string) (bool, error) {
	return m.granted, m.err
}

func TestChainDoesNotAllowOnUnavailableOrCancelledApproval(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		wantErr string
	}{
		{"unavailable channel", errors.New("no approval channel"), "approval for the write tool call failed"},
		{"cancelled channel", context.Canceled, "context canceled"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			audit := NewAuditLog(dir)
			chain := NewChain(DefaultPolicy(dir), machineApprover{granted: true, err: tt.err}, audit, "s1")
			err := chain.Check(context.Background(), "write", json.RawMessage(`{"path":"a.txt"}`))
			if err == nil {
				t.Fatal("a granted flag paired with an error must never allow the call")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("got %v, want it to contain %q", err, tt.wantErr)
			}
			entries, readErr := ReadAudit(audit.Path())
			if readErr != nil {
				t.Fatalf("audit: %v", readErr)
			}
			for _, entry := range entries {
				if entry.Decision == "allow" {
					t.Fatalf("audit recorded an allow for a failed approval channel: %+v", entry)
				}
			}
		})
	}
}
