package security

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func requireSymlinks(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(dir, "target"), filepath.Join(dir, "link")); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}
}

type capturingApprover struct {
	args string
}

func (c *capturingApprover) Ask(_ context.Context, _ string, args string) (bool, error) {
	c.args = args
	return true, nil
}

func TestSymlinkEscapeIsDeniedForReadsAndWrites(t *testing.T) {
	requireSymlinks(t)
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "id_rsa"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "home")); err != nil {
		t.Fatal(err)
	}

	if err := InWriteRoots(filepath.Join("home", "pwned.txt"), []string{root}); err == nil {
		t.Fatal("a write through a symlink out of the workspace must be denied")
	}
	policy := DefaultPolicy(root)
	for _, tool := range []string{"read", "write"} {
		path := filepath.Join("home", "id_rsa")
		if tool == "write" {
			path = filepath.Join("home", "pwned.txt")
		}
		args := mustArgs(t, fmt.Sprintf(`{"path":%q}`, path))
		if got := policy.Check(tool, args); got != Deny {
			t.Fatalf("%s through a symlink escape = %v, want Deny", tool, got)
		}
	}
}

func TestSymlinkInsideWorkspaceDoesNotRegress(t *testing.T) {
	requireSymlinks(t)
	root := t.TempDir()
	data := filepath.Join(root, "data")
	if err := os.Mkdir(data, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "a.txt"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(data, filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}

	if err := InWriteRoots(filepath.Join("alias", "b.txt"), []string{root}); err != nil {
		t.Fatalf("a symlink that stays inside the workspace must remain writable: %v", err)
	}
	policy := DefaultPolicy(root)
	args := mustArgs(t, fmt.Sprintf(`{"path":%q}`, filepath.Join("alias", "a.txt")))
	if got := policy.Check("read", args); got != Allow {
		t.Fatalf("read through an in-workspace symlink = %v, want Allow", got)
	}
}

func TestApprovalPromptShowsRealPath(t *testing.T) {
	requireSymlinks(t)
	root := t.TempDir()
	real := filepath.Join(root, "real.txt")
	if err := os.WriteFile(real, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(root, "alias.txt")); err != nil {
		t.Fatal(err)
	}
	approver := &capturingApprover{}
	chain := NewChain(DefaultPolicy(root), approver, NewAuditLog(root), "s1")
	args := mustArgs(t, `{"path":"alias.txt"}`)
	if err := chain.Check(context.Background(), "write", args); err != nil {
		t.Fatalf("write through an in-workspace symlink: %v", err)
	}
	if !strings.Contains(approver.args, "real.txt") || strings.Contains(approver.args, "alias.txt") {
		t.Fatalf("approval prompt showed %q, want the real path real.txt", approver.args)
	}
}
