package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"example.com/argentgo/internal/security"
)

// recordingGuard 只记录是否被调用，用来确认补丁策略门在拒绝时确实短路了原链路，
// 从而保证被拒绝的补丁不会进入工具执行（零写入）。
type recordingGuard struct {
	calls int
}

func (g *recordingGuard) Check(_ context.Context, _ string, _ json.RawMessage) error {
	g.calls++
	return nil
}

func patchArgs(t *testing.T, patch string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"input": patch})
	if err != nil {
		t.Fatalf("encode patch args: %v", err)
	}
	return raw
}

func TestPatchPolicyGuardOptIn(t *testing.T) {
	root := t.TempDir()
	policy := security.NewPolicy(root, security.PolicyConfig{
		DenyPaths:  []string{"**/.env"},
		WriteRoots: []string{root},
	})
	chain := security.NewChain(policy, security.NeverApprover{}, nil, "s")
	envOn := func(string) string { return "1" }
	envOff := func(string) string { return "" }

	const deniedPatch = "*** Begin Patch\n*** Update File: .env\n@@\n-old\n+new\n*** End Patch\n"
	const allowedPatch = "*** Begin Patch\n*** Update File: notes.txt\n@@\n-old\n+new\n*** End Patch\n"

	t.Run("opt-in off keeps original guard untouched", func(t *testing.T) {
		rec := &recordingGuard{}
		guard := patchPolicyGuardFor(rec, chain, envOff)
		if _, ok := guard.(*recordingGuard); !ok {
			t.Fatal("opt-in off should return the original guard unchanged")
		}
		if err := guard.Check(context.Background(), "apply_patch", patchArgs(t, deniedPatch)); err != nil {
			t.Fatalf("off path should not reject: %v", err)
		}
		if rec.calls != 1 {
			t.Fatalf("original guard should run once, got %d", rec.calls)
		}
	})

	t.Run("denied target blocks before tool runs", func(t *testing.T) {
		rec := &recordingGuard{}
		guard := patchPolicyGuardFor(rec, chain, envOn)
		err := guard.Check(context.Background(), "apply_patch", patchArgs(t, deniedPatch))
		if !errors.Is(err, security.ErrDenied) {
			t.Fatalf("expected ErrDenied, got %v", err)
		}
		if rec.calls != 0 {
			t.Fatalf("inner guard must not run for a denied patch, got %d calls", rec.calls)
		}
	})

	t.Run("allowed target passes through", func(t *testing.T) {
		rec := &recordingGuard{}
		guard := patchPolicyGuardFor(rec, chain, envOn)
		if err := guard.Check(context.Background(), "apply_patch", patchArgs(t, allowedPatch)); err != nil {
			t.Fatalf("allowed patch should pass: %v", err)
		}
		if rec.calls != 1 {
			t.Fatalf("inner guard should run once, got %d", rec.calls)
		}
	})

	t.Run("malformed input falls through to original guard", func(t *testing.T) {
		rec := &recordingGuard{}
		guard := patchPolicyGuardFor(rec, chain, envOn)
		if err := guard.Check(context.Background(), "apply_patch", patchArgs(t, "not a patch")); err != nil {
			t.Fatalf("malformed patch should not be rejected by this guard: %v", err)
		}
		if rec.calls != 1 {
			t.Fatalf("inner guard should run once, got %d", rec.calls)
		}
	})

	t.Run("non-patch tools are untouched", func(t *testing.T) {
		rec := &recordingGuard{}
		guard := patchPolicyGuardFor(rec, chain, envOn)
		if err := guard.Check(context.Background(), "write", json.RawMessage(`{"path":"x"}`)); err != nil {
			t.Fatalf("other tools should pass: %v", err)
		}
		if rec.calls != 1 {
			t.Fatalf("inner guard should run once, got %d", rec.calls)
		}
	})
}

func TestCheckPatchTargetsUsesPolicy(t *testing.T) {
	root := t.TempDir()
	if err := writeTestFile(filepath.Join(root, "a.txt")); err != nil {
		t.Fatalf("setup: %v", err)
	}
	policy := security.NewPolicy(root, security.PolicyConfig{
		DenyPaths:  []string{"**/secret.txt"},
		WriteRoots: []string{root},
	})
	chain := security.NewChain(policy, security.NeverApprover{}, nil, "s")

	if err := chain.CheckPatchTargets("apply_patch", []string{"a.txt"}, nil); err != nil {
		t.Fatalf("allowed target: %v", err)
	}
	err := chain.CheckPatchTargets("apply_patch", []string{"a.txt", "secret.txt"}, nil)
	if !errors.Is(err, security.ErrDenied) {
		t.Fatalf("denied target should return ErrDenied, got %v", err)
	}
	if err := chain.CheckPatchTargets("apply_patch", nil, nil); err != nil {
		t.Fatalf("empty targets should be a no-op: %v", err)
	}
}

func writeTestFile(path string) error {
	return os.WriteFile(path, []byte("x"), 0o644)
}
