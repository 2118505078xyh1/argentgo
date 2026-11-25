package app

import (
	"context"
	"encoding/json"
	"testing"

	"example.com/argentgo/internal/security"
)

type annotationCountingApprover struct{ asks int }

func (a *annotationCountingApprover) Ask(_ context.Context, _ string, _ string) (bool, error) {
	a.asks++
	return true, nil
}

func TestApplyMCPDestructiveApprovalOptIn(t *testing.T) {
	root := t.TempDir()
	// 把两个工具声明为只读，使策略 Allow，从而单独验证 ForceAsk 的作用。
	policy := security.NewPolicy(root, security.PolicyConfig{
		ReadOnlyTools: []string{"srv__danger", "srv__safe"},
		WriteRoots:    []string{root},
	})
	approver := &annotationCountingApprover{}
	chain := security.NewChain(policy, approver, nil, "s")
	args := json.RawMessage(`{"x":1}`)

	off := func(string) string { return "" }
	on := func(string) string { return "1" }

	// opt-in 关闭：不设置 ForceAsk，destructive 工具由策略 Allow 直接放行。
	applyMCPDestructiveApproval(chain, off, []string{"srv__danger"})
	if chain.ForceAsk != nil {
		t.Fatal("ForceAsk must stay nil when opt-in off")
	}
	if err := chain.Check(context.Background(), "srv__danger", args); err != nil {
		t.Fatal(err)
	}
	if approver.asks != 0 {
		t.Fatalf("off path should not Ask, asks=%d", approver.asks)
	}

	// opt-in 开启：destructive 工具 Allow 后仍落到 Ask。
	applyMCPDestructiveApproval(chain, on, []string{"srv__danger"})
	if chain.ForceAsk == nil || !chain.ForceAsk("srv__danger") {
		t.Fatal("ForceAsk not set for destructive tool")
	}
	if chain.ForceAsk("srv__safe") {
		t.Fatal("ForceAsk must not match other tools")
	}
	if err := chain.Check(context.Background(), "srv__danger", args); err != nil {
		t.Fatal(err)
	}
	if approver.asks != 1 {
		t.Fatalf("destructive tool should Ask, asks=%d", approver.asks)
	}

	// 非 destructive 工具不触发 Ask。
	if err := chain.Check(context.Background(), "srv__safe", args); err != nil {
		t.Fatal(err)
	}
	if approver.asks != 1 {
		t.Fatalf("non-destructive tool should not Ask, asks=%d", approver.asks)
	}
}
