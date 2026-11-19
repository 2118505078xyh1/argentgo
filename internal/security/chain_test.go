package security

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestChainDeniesBeforeAsking(t *testing.T) {
	dir := t.TempDir()
	approver := &countingApprover{allow: true}
	chain := NewChain(
		NewPolicy(dir, PolicyConfig{DenyTools: []string{"bash"}}),
		approver,
		NewAuditLog(filepath.Join(dir, "audit")),
		"s1",
	)
	err := chain.Check(context.Background(), "bash", json.RawMessage(`{"command":"ls"}`))
	if err == nil {
		t.Fatal("a denied tool must stop the run")
	}
	if !errors.Is(err, ErrDenied) || errors.Is(err, ErrRefused) {
		t.Fatalf("got %v, want ErrDenied", err)
	}
	var stop *StopError
	if !errors.As(err, &stop) || stop.Tool != "bash" || stop.Refused {
		t.Fatalf("got %#v, want a denying StopError for bash", err)
	}
	if approver.calls != 0 {
		t.Fatal("the policy hard gate must run before the approval prompt")
	}
	entries, readErr := ReadAudit(chain.Audit.Path())
	if readErr != nil || len(entries) != 1 {
		t.Fatalf("audit: %v entries=%d", readErr, len(entries))
	}
	if entries[0].Decision != "deny" || entries[0].Session != "s1" {
		t.Fatalf("audit entry = %+v", entries[0])
	}
}

func TestChainAllowsAReadWithoutPrompting(t *testing.T) {
	dir := t.TempDir()
	approver := &countingApprover{allow: false}
	chain := NewChain(DefaultPolicy(dir), approver, NewAuditLog(dir), "s1")
	if err := chain.Check(context.Background(), "read", json.RawMessage(`{"path":"a.txt"}`)); err != nil {
		t.Fatalf("read: %v", err)
	}
	if approver.calls != 0 {
		t.Fatal("a safe tool must not prompt")
	}
	entries, _ := ReadAudit(chain.Audit.Path())
	if len(entries) != 1 || entries[0].Decision != "allow" {
		t.Fatalf("audit = %+v", entries)
	}
}

func TestChainAsksAndRecordsTheAnswer(t *testing.T) {
	dir := t.TempDir()
	audit := NewAuditLog(dir)
	approved := NewChain(DefaultPolicy(dir), &countingApprover{allow: true}, audit, "s1")
	if err := approved.Check(context.Background(), "bash", json.RawMessage(`{"command":"go test"}`)); err != nil {
		t.Fatalf("approved bash: %v", err)
	}

	refused := NewChain(DefaultPolicy(dir), &countingApprover{allow: false}, audit, "s1")
	err := refused.Check(context.Background(), "write", json.RawMessage(`{"path":"a.txt","token":"abc123"}`))
	if err == nil {
		t.Fatal("a refusal must stop the run")
	}
	if !errors.Is(err, ErrRefused) || errors.Is(err, ErrDenied) {
		t.Fatalf("got %v, want ErrRefused", err)
	}
	if !strings.Contains(err.Error(), "refused") {
		t.Fatalf("error %q does not say the operator refused", err)
	}

	entries, readErr := ReadAudit(audit.Path())
	if readErr != nil || len(entries) != 2 {
		t.Fatalf("audit: %v entries=%d", readErr, len(entries))
	}
	if entries[0].Decision != "allow" || entries[0].Tool != "bash" {
		t.Fatalf("approved entry = %+v", entries[0])
	}
	if entries[1].Decision != "refused" || entries[1].Tool != "write" {
		t.Fatalf("refused entry = %+v", entries[1])
	}
	if strings.Contains(entries[1].Args, "abc123") {
		t.Fatalf("audit args leaked a token: %q", entries[1].Args)
	}
}

func TestChainCachesAnApprovalForTheSession(t *testing.T) {
	dir := t.TempDir()
	inner := &countingApprover{allow: true}
	chain := NewChain(DefaultPolicy(dir), NewCachedApprover(inner), NewAuditLog(dir), "s1")
	for i := 0; i < 2; i++ {
		if err := chain.Check(context.Background(), "bash", json.RawMessage(`{"command":"ls"}`)); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if inner.calls != 1 {
		t.Fatalf("prompted %d times, want once per session per tool", inner.calls)
	}
}

func TestChainNilApproverDenies(t *testing.T) {
	dir := t.TempDir()
	chain := NewChain(DefaultPolicy(dir), nil, NewAuditLog(dir), "s1")
	err := chain.Check(context.Background(), "bash", json.RawMessage(`{"command":"ls"}`))
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("got %v, want ErrRefused", err)
	}
}

func TestChainRespectsCancellation(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	chain := NewChain(DefaultPolicy(dir), NeverApprover{}, NewAuditLog(dir), "s1")
	if err := chain.Check(ctx, "bash", json.RawMessage(`{"command":"ls"}`)); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
}

func TestChainWithoutAuditStillGates(t *testing.T) {
	dir := t.TempDir()
	chain := &Chain{Policy: NewPolicy(dir, PolicyConfig{DenyTools: []string{"bash"}})}
	if err := chain.Check(context.Background(), "bash", json.RawMessage(`{"command":"ls"}`)); err == nil {
		t.Fatal("a nil audit log must not weaken the gate")
	}
	if err := chain.Check(context.Background(), "read", json.RawMessage(`{"path":"a.txt"}`)); err != nil {
		t.Fatalf("read: %v", err)
	}
	empty := &Chain{}
	if err := empty.Check(context.Background(), "read", json.RawMessage(`{"path":"a.txt"}`)); err != nil {
		t.Fatalf("zero chain read: %v", err)
	}
	if err := empty.Check(context.Background(), "", nil); !errors.Is(err, ErrDenied) {
		t.Fatalf("empty tool name: got %v, want ErrDenied", err)
	}
}
