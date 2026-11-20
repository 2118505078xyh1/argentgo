package security

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// makeDirectoryLink 在 link 处创建一个指向目录的链接。在 POSIX 上是符号链接；
// 在 Windows 上 junction 不需要提升权限，因此在 os.Symlink 不可用时用作回退。
func makeDirectoryLink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err == nil {
		return
	} else if filepath.Separator != '\\' {
		t.Skipf("symlinks unavailable: %v", err)
	}
	out, err := exec.Command("cmd", "/C", "mklink", "/J", link, target).CombinedOutput()
	if err != nil {
		t.Skipf("directory junctions unavailable: %v (%s)", err, strings.TrimSpace(string(out)))
	}
}

// TestProtectedSubpathCannotBeBypassedByInternalLink 验证可写根目录内指向受保护
// 子路径的链接不会让对该子路径的写入看起来可写。词法写法 (<root>/alias/id_rsa)
// 无害，但真实目标 (<root>/.ssh/id_rsa) 仍必须被拒绝。
func TestProtectedSubpathCannotBeBypassedByInternalLink(t *testing.T) {
	root := t.TempDir()
	ssh := filepath.Join(root, ".ssh")
	if err := os.MkdirAll(ssh, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	makeDirectoryLink(t, ssh, alias)

	target := filepath.Join(alias, "id_rsa")
	if blocked, reason := IsPathProtected(root, target); !blocked {
		t.Fatalf("IsPathProtected(%q) = false, %q; a link to .ssh must stay protected", target, reason)
	}

	policy := NewPolicy(root, PolicyConfig{})
	args := []byte(fmt.Sprintf(`{"path":%q}`, target))
	if got := policy.Evaluate("write", args); got.Decision != Deny {
		t.Fatalf("write through link into .ssh: decision = %v (%q), want Deny", got.Decision, got.Reason)
	}
}

// TestOrdinaryInternalLinkStaysWritable 防止过度拦截：指向普通子目录的链接必须保持
// 可写，因为其真实位置不属于任何受保护名字。
func TestOrdinaryInternalLinkStaysWritable(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	if err := os.MkdirAll(src, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	makeDirectoryLink(t, src, alias)

	target := filepath.Join(alias, "main.go")
	if blocked, reason := IsPathProtected(root, target); blocked {
		t.Fatalf("IsPathProtected(%q) = true, %q; a link to an ordinary dir must stay writable", target, reason)
	}

	policy := NewPolicy(root, PolicyConfig{})
	args := []byte(fmt.Sprintf(`{"path":%q}`, target))
	if got := policy.Evaluate("write", args); got.Decision != Ask {
		t.Fatalf("write through link into src: decision = %v (%q), want Ask", got.Decision, got.Reason)
	}
}
