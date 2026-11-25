package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"example.com/argentgo/internal/cli"
	"example.com/argentgo/internal/security"
)

// TestAutoApproverGrantsReadOnlyTools 锁定 --yolo 的便捷部分：
// 门控对普通工具和只读 shell 命令直接批准，从不
// 咨询确认回退，使 auto 模式在非命令路径上保持快捷。
// （接上行）
func TestAutoApproverGrantsReadOnlyTools(t *testing.T) {
	fallback := &countingApprover{grant: false} // 被询问时拒绝一切
	a := autoApprover{fallback: fallback}
	ctx := context.Background()
	for _, tc := range []struct{ name, args string }{
		{"read", `{"path":"a.txt"}`},
		{"glob", `{"pattern":"*.go"}`},
		{"bash", `{"command":"echo hi && git status"}`},
		{"powershell", `{"script":"Get-Content a.txt"}`},
	} {
		ok, err := a.Ask(ctx, tc.name, tc.args)
		if err != nil || !ok {
			t.Fatalf("Ask(%q, %q) = (%v, %v); want (true, nil)", tc.name, tc.args, ok, err)
		}
	}
	if fallback.calls != 0 {
		t.Fatalf("confirmation fallback called %d times for read-only tools", fallback.calls)
	}
}

// TestAutoApproverRefusesMutatingShellWithoutFallback 是核心 P1 修复：一个
// 零值 autoApprover（由直接注入它的调用方使用）绝不能
// 静默批准破坏性命令。安全失败。一个带有
// "command" 字段的非 shell 工具不受影响。
func TestAutoApproverRefusesMutatingShellWithoutFallback(t *testing.T) {
	a := autoApprover{}
	ctx := context.Background()
	for _, tc := range []struct{ name, args string }{
		{"bash", `{"command":"rm -rf /tmp/x"}`},
		{"bash", `{"command":"git push --force"}`},
		{"powershell", `{"script":"Remove-Item -Recurse C:\\tmp\\x"}`},
	} {
		ok, err := a.Ask(ctx, tc.name, tc.args)
		if err != nil || ok {
			t.Fatalf("Ask(%q, %q) = (%v, %v); want (false, nil)", tc.name, tc.args, ok, err)
		}
	}
	if ok, err := a.Ask(ctx, "read", `{"command":"rm -rf /tmp/x"}`); err != nil || !ok {
		t.Fatalf("read with a command field = (%v, %v); want granted", ok, err)
	}
}

// TestAutoApproverDelegatesMutatingShellToFallback 证明审批链仍然可用：
// 变更型 shell 命令会恰好咨询回退一次，
// 并遵循其决定。
func TestAutoApproverDelegatesMutatingShellToFallback(t *testing.T) {
	ctx := context.Background()
	for _, grant := range []bool{true, false} {
		fallback := &countingApprover{grant: grant}
		a := autoApprover{fallback: fallback}
		ok, err := a.Ask(ctx, "bash", `{"command":"rm -rf /tmp/x"}`)
		if err != nil || ok != grant {
			t.Fatalf("grant=%v: Ask = (%v, %v); want (%v, nil)", grant, ok, err, grant)
		}
		if fallback.calls != 1 {
			t.Fatalf("fallback called %d times, want exactly 1", fallback.calls)
		}
	}
}

// TestAutoChainDoesNotSilentlyRunMutatingBash 是接线层的回归
// 测试：当策略要求两个工具都审批时，同一 auto 审批器仍批准
// read，但通过 Chain.Check 对变更型 bash 命令产生拒绝。
func TestAutoChainDoesNotSilentlyRunMutatingBash(t *testing.T) {
	policy := security.NewPolicy(t.TempDir(), security.PolicyConfig{
		Rules: []security.Rule{
			{Decision: security.Ask, Tool: "bash"},
			{Decision: security.Ask, Tool: "read"},
		},
	})
	chain := security.NewChain(policy, autoApprover{}, nil, "s1")
	ctx := context.Background()

	if err := chain.Check(ctx, "read", json.RawMessage(`{"path":"a.txt"}`)); err != nil {
		t.Fatalf("read Check = %v; want allowed under auto", err)
	}
	err := chain.Check(ctx, "bash", json.RawMessage(`{"command":"rm -rf /tmp/x"}`))
	var stop *security.StopError
	if !errors.As(err, &stop) || !stop.Refused {
		t.Fatalf("mutating bash Check = %v; want a refused *security.StopError", err)
	}
}

// TestApproverForAutoWiresHumanFallback 检查 --yolo / approval=auto 获得
// 一个真实的确认回退，且它只接受 y/yes——一个 "A"
// （全部批准）的答案不得授权破坏性命令，与
// aider 的 explicit_yes_required 一致。
func TestApproverForAutoWiresHumanFallback(t *testing.T) {
	for _, tc := range []struct {
		answer string
		want   bool
	}{
		{"y\n", true},
		{"yes\n", true},
		{"A\n", false},
		{"n\n", false},
		{"\n", false},
	} {
		stdio := cli.IO{In: strings.NewReader(tc.answer), Err: &bytes.Buffer{}}
		approver := approverFor(stdio, Resolved{Approval: "auto", Storage: t.TempDir()})
		auto, ok := approver.(autoApprover)
		if !ok {
			t.Fatalf("approverFor(auto) = %T; want autoApprover", approver)
		}
		if auto.fallback == nil {
			t.Fatal("auto mode must carry a confirmation fallback")
		}
		got, err := auto.Ask(context.Background(), "bash", `{"command":"rm -rf /tmp/x"}`)
		if err != nil || got != tc.want {
			t.Fatalf("answer %q = (%v, %v); want (%v, nil)", tc.answer, got, err, tc.want)
		}
	}
}

// TestYoloAndAutoShareTheSamePath 防止两个标志产生分歧：
// --yolo 解析为 approval=auto，因此两种写法都必须经由
// 同一个带门控的审批器。
func TestYoloAndAutoShareTheSamePath(t *testing.T) {
	for _, mode := range []string{"auto", "yolo"} {
		stdio := cli.IO{In: strings.NewReader("n\n"), Err: &bytes.Buffer{}}
		approver := approverFor(stdio, Resolved{Approval: mode, Storage: t.TempDir()})
		ok, err := approver.Ask(context.Background(), "bash", `{"command":"rm -rf /tmp/x"}`)
		if err != nil || ok {
			t.Fatalf("mode %q: Ask = (%v, %v); want (false, nil) on an 'n' answer", mode, ok, err)
		}
	}
}
