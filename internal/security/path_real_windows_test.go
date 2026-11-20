//go:build windows

package security

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// makeJunction 创建一个目录 junction，无需管理员权限。filepath.EvalSymlinks 在
// Windows 上不跟随 junction，因此需要单独的测试来覆盖重解析点逃逸。
func makeJunction(t *testing.T, link, target string) {
	t.Helper()
	out, err := exec.Command("cmd", "/C", "mklink", "/J", link, target).CombinedOutput()
	if err != nil {
		t.Skipf("junction creation is unavailable: %v (%s)", err, strings.TrimSpace(string(out)))
	}
}

func TestJunctionEscapeIsDeniedForReadsAndWrites(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "id_rsa")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "home")
	makeJunction(t, link, outside)

	// 与 EvalSymlinks 不同，该辅助函数必须看穿 junction。
	real, err := ResolveRealPath(filepath.Join(link, "id_rsa"))
	if err != nil {
		t.Fatalf("ResolveRealPath through a junction: %v", err)
	}
	// 要与解析后的目标比较：在 Windows 上临时目录可能返回 8.3 短名
	// （C:\Users\RUNNER~1\...），而 ResolveRealPath 返回长形式
	// （C:\Users\runneradmin\...）。两者指向同一个文件。
	want, err := ResolveRealPath(secret)
	if err != nil {
		t.Fatalf("ResolveRealPath of the secret: %v", err)
	}
	if !strings.EqualFold(real, want) {
		t.Fatalf("junction resolved to %q, want %q", real, want)
	}

	if err := InWriteRoots(filepath.Join("home", "pwned.txt"), []string{root}); err == nil {
		t.Fatal("a write through a junction out of the workspace must be denied")
	}
	policy := DefaultPolicy(root)
	readArgs := mustArgs(t, fmt.Sprintf(`{"path":%q}`, filepath.Join("home", "id_rsa")))
	if got := policy.Check("read", readArgs); got != Deny {
		t.Fatalf("read through a junction escape = %v, want Deny", got)
	}
	writeArgs := mustArgs(t, fmt.Sprintf(`{"path":%q}`, filepath.Join("home", "pwned.txt")))
	if got := policy.Check("write", writeArgs); got != Deny {
		t.Fatalf("write through a junction escape = %v, want Deny", got)
	}
}

func TestApprovalPromptShowsRealPathForJunction(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	if err := os.Mkdir(data, 0o755); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(data, "real.txt")
	if err := os.WriteFile(real, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "alias")
	makeJunction(t, link, data)

	approver := &capturingApprover{}
	chain := NewChain(DefaultPolicy(root), approver, NewAuditLog(root), "s1")
	args := mustArgs(t, fmt.Sprintf(`{"path":%q}`, filepath.Join("alias", "real.txt")))
	if err := chain.Check(context.Background(), "write", args); err != nil {
		t.Fatalf("write through an in-workspace junction: %v", err)
	}
	if !strings.Contains(approver.args, "real.txt") || strings.Contains(approver.args, "alias") {
		t.Fatalf("approval prompt showed %q, want the real path real.txt", approver.args)
	}
}

func TestJunctionInsideWorkspaceDoesNotRegress(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	if err := os.Mkdir(data, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "a.txt"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	makeJunction(t, filepath.Join(root, "alias"), data)

	if err := InWriteRoots(filepath.Join("alias", "b.txt"), []string{root}); err != nil {
		t.Fatalf("a junction that stays inside the workspace must remain writable: %v", err)
	}
	policy := DefaultPolicy(root)
	args := mustArgs(t, fmt.Sprintf(`{"path":%q}`, filepath.Join("alias", "a.txt")))
	if got := policy.Check("read", args); got != Allow {
		t.Fatalf("read through an in-workspace junction = %v, want Allow", got)
	}
}
