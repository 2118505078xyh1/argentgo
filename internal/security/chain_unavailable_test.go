package security

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestUnavailableApproverAsk 固定空对象的原始回答及其分类。
// 该哨兵是非 context 错误，因此 ClassifyApproval 必须保持其现有规则，
// 把它映射到 ApprovalUnavailable（绝不能是 Rejected）。
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

	// 已取消的 context 仍然优先，与其他审批器完全一致。
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err = UnavailableApprover{}.Ask(ctx, "bash", `{}`)
	if got || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Ask = %v, %v; want false, context.Canceled", got, err)
	}
}

// TestChainExplicitRefusalStaysRefused 守护未改变的分支：真正的操作员"否"仍然是
// 终结性 StopError，并被审计为 "refused"。纯 bool 否决和显式的
// NeverApprover 空对象都必须保持这一点。
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

// TestChainDegradedChannelErrorRecordsUnavailable 覆盖通道损坏的审批器：
// 该错误是普通的非 context 错误，因此链审计为不可用通道（而非否决），
// 并返回不带终结语句的错误。
func TestChainDegradedChannelErrorRecordsUnavailable(t *testing.T) {
	dir := t.TempDir()
	audit := NewAuditLog(dir)
	chain := NewChain(DefaultPolicy(dir), machineApprover{err: errors.New("stdin broken")}, audit, "s1")
	err := chain.Check(context.Background(), "write", json.RawMessage(`{"path":"a.txt"}`))
	if err == nil {
		t.Fatal("a broken approval channel must not allow the call")
	}
	// 链包装原始通道错误，而不是用哨兵替换它，因此按文本匹配。
	// ClassifyApproval 把任何非 context 错误映射为 ApprovalUnavailable；
	// 这就是审计日志记录的分类。
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

// TestChainCancelledApprovalIsNotUnavailable 锁定两种失败即拒绝结果之间的边界：
// 取消保留其 context 错误，不会在审计日志中被错误标记为不可用通道。
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

// TestNilApproverChainViaConstructor 让 NewChain 的空对象行为与直接接线的
// UnavailableApprover 一致，使两条路径不会产生偏差。
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
