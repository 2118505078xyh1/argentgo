package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

func TestFormatInspectionReason(t *testing.T) {
	tests := []struct {
		name string
		v    security.Verdict
		want string
	}{
		{
			name: "reason and finding ID",
			v:    security.Verdict{Reason: "destructive shell command: rm -rf /", FindingID: "SEC-1234ABCD-DENY"},
			want: "[security SEC-1234ABCD-DENY: destructive shell command: rm -rf /]",
		},
		{
			name: "reason only",
			v:    security.Verdict{Reason: "needs a human"},
			want: "[security: needs a human]",
		},
		{
			name: "finding ID only when reason is empty",
			v:    security.Verdict{FindingID: "SEC-DEADBEEF-APPROVAL"},
			want: "[security SEC-DEADBEEF-APPROVAL]",
		},
		{
			name: "empty verdict has no context",
			v:    security.Verdict{},
			want: "",
		},
		{
			name: "whitespace-only fields have no context",
			v:    security.Verdict{Reason: "  \t ", FindingID: "\n"},
			want: "",
		},
		{
			name: "CJK reason and finding ID are preserved",
			v:    security.Verdict{Reason: "检测到破坏性命令：删除目录", FindingID: "SEC-中文-01"},
			want: "[security SEC-中文-01: 检测到破坏性命令：删除目录]",
		},
		{
			name: "multi-line reason folds to one printable line",
			v:    security.Verdict{Reason: "line one\nline\ttwo\x00end", FindingID: "SEC-1"},
			want: "[security SEC-1: line one line twoend]",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatInspectionReason(tc.v); got != tc.want {
				t.Fatalf("formatInspectionReason = %q, want %q", got, tc.want)
			}
		})
	}
}

// reasonRecordingApprover records what the guard handed it. It implements both
// the plain Approver and the optional reason-aware seam so a test can tell
// which path was taken.
type reasonRecordingApprover struct {
	grant      bool
	calls      int
	plainCalls int
	name       string
	args       string
	reason     string
}

func (a *reasonRecordingApprover) AskWithReason(_ context.Context, name, args, reason string) (bool, error) {
	a.calls++
	a.name, a.args, a.reason = name, args, reason
	return a.grant, nil
}

func (a *reasonRecordingApprover) Ask(_ context.Context, name, args string) (bool, error) {
	a.plainCalls++
	a.name, a.args = name, args
	return a.grant, nil
}

func TestAskInspectionApprovalPassesReasonWithUnchangedInputs(t *testing.T) {
	approver := &reasonRecordingApprover{grant: true}
	args := json.RawMessage(`{"command":"rm -rf /tmp/x"}`)
	v := security.Verdict{
		Action:    security.InspectRequireApproval,
		Reason:    "destructive shell command: rm",
		FindingID: "SEC-ABCD",
	}

	ok, err := askInspectionApproval(context.Background(), approver, "bash", args, v)
	if err != nil || !ok {
		t.Fatalf("Ask = (%v, %v), want (true, nil)", ok, err)
	}
	if approver.plainCalls != 0 || approver.calls != 1 {
		t.Fatalf("reason-aware path called %d times, plain path %d; want 1/0", approver.calls, approver.plainCalls)
	}
	if approver.name != "bash" || approver.args != string(args) {
		t.Fatalf("cache inputs changed: name=%q args=%q", approver.name, approver.args)
	}
	if want := formatInspectionReason(v); approver.reason != want {
		t.Fatalf("reason = %q, want %q", approver.reason, want)
	}
	// The finding ID must be part of the context, not just the free-form reason.
	if !strings.Contains(approver.reason, "SEC-ABCD") {
		t.Fatalf("reason lacks finding ID: %q", approver.reason)
	}
}

func TestAskInspectionApprovalFallsBackToPlainAsk(t *testing.T) {
	approver := &countingApprover{grant: true}
	args := json.RawMessage(`{"path":"a.txt"}`)
	v := security.Verdict{Action: security.InspectRequireApproval, Reason: "needs a human"}

	ok, err := askInspectionApproval(context.Background(), approver, "write", args, v)
	if err != nil || !ok || approver.calls != 1 {
		t.Fatalf("Ask = (%v, %v), calls=%d; want (true, nil), 1", ok, err, approver.calls)
	}
}

// TestInspectionReasonDoesNotDriftApprovalCache is the regression guard for
// the audit finding: a reason that changes between identical tool calls must
// NOT change the CachedApprover key. If a [security: ...] prefix were folded
// into name or args, the second and third calls would miss the cache and the
// inner approver would be asked three times instead of once.
func TestInspectionReasonDoesNotDriftApprovalCache(t *testing.T) {
	inner := &countingApprover{grant: true}
	cached := security.NewCachedApprover(inner)
	ctx := context.Background()
	args := json.RawMessage(`{"command":"echo hi"}`)

	verdicts := []security.Verdict{
		{Action: security.InspectRequireApproval, Reason: "first reason", FindingID: "SEC-1"},
		{Action: security.InspectRequireApproval, Reason: "完全不同的理由", FindingID: "SEC-2"},
		{Action: security.InspectRequireApproval, Reason: "", FindingID: "SEC-3"},
	}
	for _, v := range verdicts {
		ok, err := askInspectionApproval(ctx, cached, "bash", args, v)
		if err != nil || !ok {
			t.Fatalf("Ask(reason=%q) = (%v, %v), want (true, nil)", v.Reason, ok, err)
		}
	}
	if inner.calls != 1 {
		t.Fatalf("inner approver called %d times; reason must not enter the cache key", inner.calls)
	}
}

func TestInspectionGuardCheckPassesReasonToAwareApprover(t *testing.T) {
	approver := &reasonRecordingApprover{grant: true}
	chainApprover := &countingApprover{grant: false}
	guard := &inspectionGuard{
		manager: managerWith(verdictInspector{
			name: "approval",
			verdict: security.Verdict{
				Action:     security.InspectRequireApproval,
				Reason:     "destructive shell command: rm",
				FindingID:  "SEC-FIND-1",
				Confidence: 1,
			},
		}),
		chain:    askChain(t.TempDir(), chainApprover),
		approver: approver,
	}

	args := json.RawMessage(`{"command":"rm -rf /tmp/x"}`)
	if err := guard.Check(context.Background(), "bash", args); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if approver.calls != 1 || approver.plainCalls != 0 {
		t.Fatalf("aware calls=%d plain calls=%d, want 1/0", approver.calls, approver.plainCalls)
	}
	if approver.name != "bash" || approver.args != string(args) {
		t.Fatalf("Check changed cache inputs: name=%q args=%q", approver.name, approver.args)
	}
	if !strings.Contains(approver.reason, "SEC-FIND-1") || !strings.Contains(approver.reason, "destructive shell command") {
		t.Fatalf("Check reason = %q, want finding ID and reason", approver.reason)
	}
	// The replayed chain call must not prompt a second time.
	if chainApprover.calls != 0 {
		t.Fatalf("chain re-prompted %d times", chainApprover.calls)
	}
}

func TestAskInspectionApprovalNilApproverIsUnavailable(t *testing.T) {
	ok, err := askInspectionApproval(context.Background(), nil, "bash", json.RawMessage(`{}`), security.Verdict{})
	if ok || !errors.Is(err, security.ErrApprovalUnavailable) {
		t.Fatalf("nil approver = (%v, %v), want unavailable", ok, err)
	}
}
