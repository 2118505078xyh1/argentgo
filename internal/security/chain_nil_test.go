package security

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// TestNilChainFailsClosedWithoutPanicking covers a nil *Chain used directly as
// a Guard. Policy(), record() and the plan-command-guard branch all honour a
// nil receiver, so the approval branch must be nil-safe too: an Ask decision
// must degrade to "approval unavailable" instead of dereferencing c.Approver
// and crashing the caller.
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
		// "bash" is mutating and falls through to approval. With no chain there
		// is no approver, so this must be the recoverable "unavailable" error
		// and never a StopError claiming the operator refused.
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
