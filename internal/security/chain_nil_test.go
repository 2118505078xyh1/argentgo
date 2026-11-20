package security

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// TestNilChainFailsClosedWithoutPanicking 覆盖直接用作 Guard 的 nil *Chain。
// Policy()、record() 和计划命令守卫分支都兼容 nil 接收者，
// 因此审批分支也必须 nil 安全：Ask 决定必须降级为"审批不可用"，
// 而不是解引用 c.Approver 并让调用方崩溃。
func TestNilChainFailsClosedWithoutPanicking(t *testing.T) {
	var chain *Chain
	ctx := context.Background()

	t.Run("allow stays allow", func(t *testing.T) {
		if err := chain.Check(ctx, "read", json.RawMessage(`{"path":"a.txt"}`)); err != nil {
			t.Fatalf("nil chain read: %v", err)
		}
	})

	t.Run("deny stays deny", func(t *testing.T) {
		err := chain.Check(ctx, "read", json.RawMessage(`{"path":"../escape.txt"}`))
		if !errors.Is(err, ErrDenied) {
			t.Fatalf("nil chain traversal: got %v, want ErrDenied", err)
		}
	})

	t.Run("ask is unavailable, not a refusal", func(t *testing.T) {
		// "bash" 是修改类工具，会落到审批。没有链就没有审批器，
		// 因此这必须是可恢复的 "unavailable" 错误，
		// 绝不能是声称操作员否决的 StopError。
		err := chain.Check(ctx, "bash", json.RawMessage(`{"command":"ls"}`))
		if err == nil {
			t.Fatal("nil chain must not approve a mutating call")
		}
		if errors.Is(err, ErrDenied) || errors.Is(err, ErrRefused) {
			t.Fatalf("nil chain ask must not be a hard deny/refusal: %v", err)
		}
		if !errors.Is(err, ErrApprovalUnavailable) {
			t.Fatalf("nil chain ask = %v, want ErrApprovalUnavailable", err)
		}
	})

	t.Run("empty tool name is denied", func(t *testing.T) {
		if err := chain.Check(ctx, "", nil); !errors.Is(err, ErrDenied) {
			t.Fatalf("nil chain empty name: got %v, want ErrDenied", err)
		}
	})

	t.Run("restore path is gated", func(t *testing.T) {
		err := chain.CheckRestore(ctx, []string{"../outside.txt"})
		if !errors.Is(err, ErrDenied) {
			t.Fatalf("nil chain restore escape: got %v, want ErrDenied", err)
		}
	})

	t.Run("cancelled ask stays cancelled", func(t *testing.T) {
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		err := chain.Check(cancelled, "bash", json.RawMessage(`{"command":"ls"}`))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("nil chain cancelled ask: got %v, want context.Canceled", err)
		}
	})
}
