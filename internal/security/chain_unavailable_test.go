package security

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestUnavailableApproverAsk pins the null object's raw answer and its
// classification. The sentinel is a non-context error, so ClassifyApproval must
// keep its existing rule and map it to ApprovalUnavailable (never Rejected).
func TestUnavailableApproverAsk(t *testing.T) {
	got, err := UnavailableApprover{}.Ask(context.Background(), "bash", `{}`)
	if got {
		t.Fatal("UnavailableApprover must not grant")
	}
	if !errors.Is(err, ErrApprovalUnavailable) {
		t.Fatalf("Ask error = %v, want ErrApprovalUnavailable", err)
	}
	if outcome := ClassifyApproval(got, err); outcome != ApprovalUnavailable {
		t.Fatalf("ClassifyApproval = %q, want %q", outcome, ApprovalUnavailable)
	}
	if outcome := ApprovalUnavailable; outcome.Allows() {
		t.Fatal("an unavailable channel must never be treated as consent")
	}

	// A cancelled context still wins, exactly as with the other approvers.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err = UnavailableApprover{}.Ask(ctx, "bash", `{}`)
	if got || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Ask = %v, %v; want false, context.Canceled", got, err)
	}
}

// TestChainExplicitRefusalStaysRefused guards the unchanged branch: a real
// operator "no" is still a final StopError audited as "refused". Both the plain
// bool-only refusal and the explicit NeverApprover null object must keep it.
func TestChainExplicitRefusalStaysRefused(t *testing.T) {
	for _, tc := range []struct {
		name     string
		approver Approver
	}{
		{"bool only refusal", &countingApprover{allow: false}},
		{"NeverApprover", NeverApprover{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			audit := NewAuditLog(dir)
			chain := NewChain(DefaultPolicy(dir), tc.approver, audit, "s1")
			err := chain.Check(context.Background(), "write", json.RawMessage(`{"path":"a.txt"}`))
			if !errors.Is(err, ErrRefused) || errors.Is(err, ErrDenied) {
				t.Fatalf("got %v, want ErrRefused only", err)
			}
			var stop *StopError
			if !errors.As(err, &stop) || !stop.Refused {
				t.Fatalf("got %v, want a refusing StopError", err)
			}
			if !strings.Contains(err.Error(), finalitySentence) {
				t.Fatalf("a real refusal must carry finality: %q", err.Error())
			}
			if errors.Is(err, ErrApprovalUnavailable) {
				t.Fatalf("a real refusal must not be an unavailable channel: %v", err)
			}
			entries, readErr := ReadAudit(audit.Path())
			if readErr != nil || len(entries) != 1 {
				t.Fatalf("audit: %v entries=%d", readErr, len(entries))
			}
			if entries[0].Decision != "refused" {
				t.Fatalf("audit entry = %+v", entries[0])
			}
		})
	}
}

// TestChainDegradedChannelErrorRecordsUnavailable covers an approver whose
// channel broke: the error is a plain non-context error, so the chain audits an
// unavailable channel (not a refusal) and returns an error without finality.
func TestChainDegradedChannelErrorRecordsUnavailable(t *testing.T) {
	dir := t.TempDir()
	audit := NewAuditLog(dir)
	chain := NewChain(DefaultPolicy(dir), machineApprover{err: errors.New("stdin broken")}, audit, "s1")
	err := chain.Check(context.Background(), "write", json.RawMessage(`{"path":"a.txt"}`))
	if err == nil {
		t.Fatal("a broken approval channel must not allow the call")
	}
	// The chain wraps the original channel error rather than replacing it with
	// the sentinel, so match on its text. ClassifyApproval maps any non-context
	// error to ApprovalUnavailable; that is the classification the audit logs.
	if !strings.Contains(err.Error(), "stdin broken") {
		t.Fatalf("got %v, want it to wrap the original channel error", err)
	}
	if errors.Is(err, ErrRefused) || errors.Is(err, ErrDenied) {
		t.Fatalf("a broken channel must not be a refusal or a policy deny: %v", err)
	}
	var stop *StopError
	if errors.As(err, &stop) {
		t.Fatalf("a broken channel must not be a StopError: %#v", err)
	}
	if strings.Contains(err.Error(), finalitySentence) {
		t.Fatalf("a broken channel must not carry finality: %q", err.Error())
	}
	entries, readErr := ReadAudit(audit.Path())
	if readErr != nil || len(entries) != 1 {
		t.Fatalf("audit: %v entries=%d", readErr, len(entries))
	}
	if entries[0].Decision != "unavailable" {
		t.Fatalf("audit entry = %+v", entries[0])
	}
	if strings.Contains(entries[0].Reason, "refused") {
		t.Fatalf("audit reason must not claim a refusal: %q", entries[0].Reason)
	}
}

// TestChainCancelledApprovalIsNotUnavailable locks the boundary between the two
// fail-closed outcomes: a cancellation keeps its context error and is not
// mislabeled as an unavailable channel in the audit log.
func TestChainCancelledApprovalIsNotUnavailable(t *testing.T) {
	dir := t.TempDir()
	audit := NewAuditLog(dir)
	chain := NewChain(DefaultPolicy(dir), machineApprover{granted: true, err: context.Canceled}, audit, "s1")
	err := chain.Check(context.Background(), "write", json.RawMessage(`{"path":"a.txt"}`))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	entries, readErr := ReadAudit(audit.Path())
	if readErr != nil {
		t.Fatalf("audit: %v", readErr)
	}
	for _, entry := range entries {
		if entry.Decision == "unavailable" || entry.Decision == "refused" || entry.Decision == "allow" {
			t.Fatalf("cancelled approval got an unexpected decision: %+v", entry)
		}
	}
}

// TestNilApproverChainViaConstructor matches NewChain's null-object behaviour
// to a directly wired UnavailableApprover, so the two paths cannot drift.
func TestNilApproverChainMatchesUnavailableApprover(t *testing.T) {
	toolErr := func(approver Approver) string {
		t.Helper()
		dir := t.TempDir()
		chain := NewChain(DefaultPolicy(dir), approver, nil, "s1")
		err := chain.Check(context.Background(), "bash", json.RawMessage(`{"command":"ls"}`))
		if err == nil {
			t.Fatal("both paths must fail closed")
		}
		return err.Error()
	}
	got := toolErr(nil)
	want := toolErr(UnavailableApprover{})
	if got != want {
		t.Fatalf("nil approver error %q differs from UnavailableApprover error %q", got, want)
	}
}
