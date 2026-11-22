package security

import (
	"context"
	"testing"
)

func TestHookApprovalStampScopedToToolAndArgs(t *testing.T) {
	ctx := WithHookApproval(context.Background(), "write", `{"path":"x"}`)
	if !HookApproved(ctx, "write", `{"path":"x"}`) {
		t.Fatal("matching stamp should be approved")
	}
	if HookApproved(ctx, "read", `{"path":"x"}`) {
		t.Fatal("stamp must be scoped to tool")
	}
	if HookApproved(ctx, "write", `{"path":"y"}`) {
		t.Fatal("stamp must be scoped to args")
	}
	if HookApproved(context.Background(), "write", `{"path":"x"}`) {
		t.Fatal("no stamp should never approve")
	}
}

// preapprovalCountingApprover records whether Ask was called.
type preapprovalCountingApprover struct{ asks int }

func (a *preapprovalCountingApprover) Ask(_ context.Context, _ string, _ string) (bool, error) {
	a.asks++
	return true, nil
}

func TestChainSkipsAskWhenHookPreApproved(t *testing.T) {
	root := t.TempDir()
	policy := NewPolicy(root, PolicyConfig{WriteRoots: []string{root}})
	approver := &preapprovalCountingApprover{}
	chain := NewChain(policy, approver, nil, "s")
	args := []byte(`{"path":"x"}`)

	// 无章：走正常 Ask。
	if err := chain.Check(context.Background(), "write", args); err != nil {
		t.Fatalf("normal check: %v", err)
	}
	if approver.asks != 1 {
		t.Fatalf("asks = %d, want 1", approver.asks)
	}

	// 有章：跳过 Ask，但返回 nil（策略与审计照跑）。
	ctx := WithHookApproval(context.Background(), "write", string(args))
	if err := chain.Check(ctx, "write", args); err != nil {
		t.Fatalf("pre-approved check: %v", err)
	}
	if approver.asks != 1 {
		t.Fatalf("pre-approved call must not Ask; asks = %d", approver.asks)
	}

	// 章不匹配：仍走 Ask。
	ctx = WithHookApproval(context.Background(), "write", `{"path":"other"}`)
	if err := chain.Check(ctx, "write", args); err != nil {
		t.Fatalf("mismatched stamp check: %v", err)
	}
	if approver.asks != 2 {
		t.Fatalf("mismatched stamp must Ask; asks = %d", approver.asks)
	}

}
