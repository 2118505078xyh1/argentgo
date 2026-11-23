package security

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// autoYesApprover 模拟 YOLO/自动批准器：愿意说是，但不是真人交互。
type autoYesApprover struct{ asked int }

func (a *autoYesApprover) Ask(context.Context, string, string) (bool, error) {
	a.asked++
	return true, nil
}
func (a *autoYesApprover) InteractiveReply() bool { return false }

func TestRequireHumanBypassesAutoApproval(t *testing.T) {
	dir := t.TempDir()
	policy := NewPolicy(dir, PolicyConfig{Rules: []Rule{
		{Decision: Allow, Tool: "write", RequireHuman: true},
	}})
	approver := &autoYesApprover{}
	chain := NewChain(policy, approver, NewAuditLog(dir), "s1")
	err := chain.Check(context.Background(), "write", json.RawMessage(`{"path":"a.txt"}`))
	var stop *StopError
	if !errors.As(err, &stop) {
		t.Fatalf("require-human call must be denied without a human channel, got %v", err)
	}
	if approver.asked != 0 {
		t.Fatalf("must not even ask an auto approver, asked=%d", approver.asked)
	}
}

func TestRequireHumanBypassesCachedGrants(t *testing.T) {
	dir := t.TempDir()
	chain := NewChain(DefaultPolicy(dir), nil, NewAuditLog(dir), "s1")
	// 无标志时先取得一次真人批准并写入缓存。
	human := NewCachedApprover(humanAlwaysApprover{})
	normalChain := NewChain(DefaultPolicy(dir), human, NewAuditLog(dir), "s1")
	if err := normalChain.Check(context.Background(), "write", json.RawMessage(`{"path":"a.txt"}`)); err != nil {
		t.Fatalf("baseline approval: %v", err)
	}

	// 带 RequireHuman 标志的同一调用不得复用该缓存；这里用无交互通道
	// 验证它确实走到真人判定而非缓存命中。
	ctx := WithRequireHuman(context.Background())
	err := chain.Check(ctx, "write", json.RawMessage(`{"path":"a.txt"}`))
	var stop *StopError
	if !errors.As(err, &stop) {
		t.Fatalf("require-human must bypass cache, got %v", err)
	}

	// 不带标志的行为保持不变：缓存批准直接放行。
	if err := normalChain.Check(context.Background(), "write", json.RawMessage(`{"path":"a.txt"}`)); err != nil {
		t.Fatalf("non-marked call must still use the cache: %v", err)
	}
}

// onceApprover 只在第一次回答"是"，用于确认缓存被真正绕过。
type onceApprover struct{ n int }

func (a *onceApprover) Ask(context.Context, string, string) (bool, error) {
	a.n++
	return a.n == 1, nil
}

func TestRequireHumanCachedApproverBypassesCache(t *testing.T) {
	cache := NewCachedApprover(&onceApprover{})
	ctx := context.Background()
	if ok, err := cache.Ask(ctx, "write", "{}"); !ok || err != nil {
		t.Fatalf("first answer must be granted: ok=%v err=%v", ok, err)
	}
	// 带标记时不得命中缓存；inner 的第二次回答是否决。
	if ok, err := cache.Ask(WithRequireHuman(ctx), "write", "{}"); ok || err != nil {
		t.Fatalf("require-human must bypass cached grant: ok=%v err=%v", ok, err)
	}
}

func TestRequireHumanPersistentApproverBypassesAlways(t *testing.T) {
	dir := t.TempDir()
	inner := &onceApprover{}
	approver := NewFileBackedScopedApprover(inner, dir, nil)
	ctx := context.Background()
	if ok, err := approver.Ask(ctx, "write", "{}"); !ok || err != nil {
		t.Fatalf("first answer must be granted: ok=%v err=%v", ok, err)
	}
	// 第一次被记住为 always；RequireHuman 必须跳过它再次询问 inner。
	if ok, err := approver.Ask(WithRequireHuman(ctx), "write", "{}"); ok || err != nil {
		t.Fatalf("require-human must bypass persistent always: ok=%v err=%v", ok, err)
	}
}

func TestRequireHumanIdempotentContext(t *testing.T) {
	ctx := WithRequireHuman(WithRequireHuman(nil))
	if !RequireHumanFrom(ctx) {
		t.Fatal("nested marking must stay marked")
	}
	if RequireHumanFrom(context.Background()) {
		t.Fatal("plain context must be unmarked")
	}
}
