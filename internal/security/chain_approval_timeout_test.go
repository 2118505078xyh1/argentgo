package security

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// blockingApprover 模拟无人应答的审批通道：在 ctx 到期前一直阻塞，到期后把
// ctx 的错误原样返回。内置审批器（PromptApprover 等）都遵循这一约定。
type blockingApprover struct {
	seenErr error
}

func (a *blockingApprover) Ask(ctx context.Context, name string, args string) (bool, error) {
	<-ctx.Done()
	a.seenErr = ctx.Err()
	return false, a.seenErr
}

// blockingStagedApprover 让分阶段审批的每一轮都无人应答，同时记录普通 Ask
// 是否被误调用（opt-in 打开后应当只走 AskStage）。
type blockingStagedApprover struct {
	blockingApprover
	stageCalls int
}

func (a *blockingStagedApprover) AskStage(ctx context.Context, name string, args string, stage ApprovalStage, summary string) (ApprovalDecision, error) {
	a.stageCalls++
	<-ctx.Done()
	a.seenErr = ctx.Err()
	return ApprovalDecision{}, a.seenErr
}

func (a *blockingStagedApprover) RememberScope(scopes []string) {}

// assertNoAllowDecision 确认审计日志中不存在"批准"决定：超时是失败即拒绝的
// 取消，绝不能被记成操作员同意。
func assertNoAllowDecision(t *testing.T, audit *AuditLog) {
	t.Helper()
	entries, err := ReadAudit(audit.Path())
	if err != nil {
		t.Fatalf("读取审计日志失败: %v", err)
	}
	for _, entry := range entries {
		if entry.Decision == "allow" {
			t.Fatalf("超时审批被记成了 allow: %+v", entry)
		}
	}
}

// TestChainAskTimeoutFailsClosed 验证背景 ctx 下无人应答时，审批等待会按注入的
// 短超时按时返回，且失败即拒绝地归为 ApprovalCancelled，永远不会变成批准。
func TestChainAskTimeoutFailsClosed(t *testing.T) {
	const (
		wait  = 100 * time.Millisecond
		bound = 5 * time.Second
	)
	dir := t.TempDir()
	audit := NewAuditLog(dir)
	approver := &blockingApprover{}
	chain := NewChain(DefaultPolicy(dir), approver, audit, "s1")
	chain.ApprovalTimeout = wait

	start := time.Now()
	err := chain.Check(context.Background(), "write", json.RawMessage(`{"path":"a.txt"}`))
	elapsed := time.Since(start)
	if elapsed > bound {
		t.Fatalf("审批等待未在超时后按时返回，耗时 %v（上限 %v）", elapsed, bound)
	}
	if elapsed < wait/2 {
		t.Fatalf("审批等待过早返回，耗时 %v（配置的超时 %v）", elapsed, wait)
	}
	if err == nil {
		t.Fatal("无人应答的审批超时必须返回错误，不能放行工具调用")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v，期望包装 DeadlineExceeded", err)
	}
	if outcome := ClassifyApproval(false, err); outcome != ApprovalCancelled {
		t.Fatalf("ClassifyApproval = %q，期望 %q（失败即拒绝）", outcome, ApprovalCancelled)
	}
	if ApprovalCancelled.Allows() {
		t.Fatal("ApprovalCancelled 绝不能被视为批准")
	}
	if !errors.Is(approver.seenErr, context.DeadlineExceeded) {
		t.Fatalf("审批器看到的 ctx 错误 = %v，期望 DeadlineExceeded", approver.seenErr)
	}
	assertNoAllowDecision(t, audit)
}

// TestChainStagedAskTimeoutFailsClosed 确认分阶段审批路径同样受超时约束：无人
// 应答时整个交互按时结束，结果仍是取消而非批准。
func TestChainStagedAskTimeoutFailsClosed(t *testing.T) {
	const (
		wait  = 100 * time.Millisecond
		bound = 5 * time.Second
	)
	dir := t.TempDir()
	audit := NewAuditLog(dir)
	approver := &blockingStagedApprover{}
	chain := NewChain(DefaultPolicy(dir), approver, audit, "s1")
	chain.ApprovalTimeout = wait
	chain.EnableStagedApproval(true)

	start := time.Now()
	err := chain.Check(context.Background(), "write", json.RawMessage(`{"path":"a.txt"}`))
	elapsed := time.Since(start)
	if elapsed > bound {
		t.Fatalf("分阶段审批等待未在超时后按时返回，耗时 %v（上限 %v）", elapsed, bound)
	}
	if elapsed < wait/2 {
		t.Fatalf("分阶段审批等待过早返回，耗时 %v（配置的超时 %v）", elapsed, wait)
	}
	if err == nil {
		t.Fatal("无人应答的分阶段审批超时必须返回错误")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v，期望包装 DeadlineExceeded", err)
	}
	if outcome := ClassifyApproval(false, err); outcome != ApprovalCancelled {
		t.Fatalf("ClassifyApproval = %q，期望 %q", outcome, ApprovalCancelled)
	}
	if approver.stageCalls == 0 {
		t.Fatal("opt-in 打开后应走 AskStage，却一次都没调用")
	}
	assertNoAllowDecision(t, audit)
}

// TestChainApprovalTimeoutDoesNotPolluteParent 验证派生的审批 context 只作用于
// Ask：审批超时后父 ctx 未被取消、原有 deadline 未被缩短，后续步骤仍能正常用
// 同一个父 ctx 完成另一次调用。
func TestChainApprovalTimeoutDoesNotPolluteParent(t *testing.T) {
	const (
		wait        = 80 * time.Millisecond
		parentTTL   = 10 * time.Second
		bound       = 5 * time.Second
		parentSlack = 2 * time.Second
	)
	dir := t.TempDir()
	parent, cancelParent := context.WithTimeout(context.Background(), parentTTL)
	defer cancelParent()
	parentDeadline, ok := parent.Deadline()
	if !ok {
		t.Fatal("父 context 缺少 deadline")
	}

	audit := NewAuditLog(dir)
	blocking := NewChain(DefaultPolicy(dir), &blockingApprover{}, audit, "s1")
	blocking.ApprovalTimeout = wait
	if err := blocking.Check(parent, "write", json.RawMessage(`{"path":"a.txt"}`)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("超时审批 err = %v，期望 DeadlineExceeded", err)
	}

	if err := parent.Err(); err != nil {
		t.Fatalf("审批超时污染了父 context，parent.Err() = %v", err)
	}
	deadlineAfter, ok := parent.Deadline()
	if !ok {
		t.Fatal("审批超时后父 context 丢失了 deadline")
	}
	if delta := parentDeadline.Sub(deadlineAfter); delta < -parentSlack || delta > parentSlack {
		t.Fatalf("父 context 的 deadline 被改动：原 %v，现 %v", parentDeadline, deadlineAfter)
	}

	// 用同一个父 ctx 再做一次正常审批：应当及时通过，不受上次超时影响。
	quick := NewChain(DefaultPolicy(dir), &countingApprover{allow: true}, audit, "s2")
	done := make(chan error, 1)
	go func() {
		done <- quick.Check(parent, "bash", json.RawMessage(`{"command":"go test"}`))
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("父 context 上的后续调用失败: %v", err)
		}
	case <-time.After(bound):
		t.Fatal("父 context 上的后续调用未在限定时间内返回")
	}
}

// TestChainTimelyApprovalKeepsBaseline 确认及时应答的路径逐字节保持原有行为：
// 默认超时下立即批准仍然放行，审计决定仍为 allow，错误为 nil。
func TestChainTimelyApprovalKeepsBaseline(t *testing.T) {
	dir := t.TempDir()
	audit := NewAuditLog(dir)
	approver := &countingApprover{allow: true}
	chain := NewChain(DefaultPolicy(dir), approver, audit, "s1")
	if chain.ApprovalTimeout != 0 {
		t.Fatalf("NewChain 的 ApprovalTimeout 零值应为 0，得到 %v", chain.ApprovalTimeout)
	}
	if err := chain.Check(context.Background(), "bash", json.RawMessage(`{"command":"go test"}`)); err != nil {
		t.Fatalf("及时批准仍应放行，得到 %v", err)
	}
	if approver.calls != 1 {
		t.Fatalf("Ask 调用次数 = %d，期望 1", approver.calls)
	}
	entries, err := ReadAudit(audit.Path())
	if err != nil || len(entries) != 1 {
		t.Fatalf("审计: %v，条目数 %d", err, len(entries))
	}
	if entries[0].Decision != "allow" || entries[0].Reason != "approved by the operator" {
		t.Fatalf("审计条目 = %+v，与原行为不一致", entries[0])
	}
}

// TestApprovalTimeoutConstants 锁定默认 5 分钟与上限 30 分钟这两个安全常量。
func TestApprovalTimeoutConstants(t *testing.T) {
	if defaultApprovalTimeout != 5*time.Minute {
		t.Fatalf("默认审批超时 = %v，期望 5m", defaultApprovalTimeout)
	}
	if maxApprovalTimeout != 30*time.Minute {
		t.Fatalf("审批超时上限 = %v，期望 30m", maxApprovalTimeout)
	}
}
