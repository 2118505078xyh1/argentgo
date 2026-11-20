package security

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// makeDirectoryLink creates a link to a directory at link. On POSIX that is a
// symlink; on Windows a junction needs no elevated privilege, so it is used as
// the fallback when os.Symlink is unavailable.
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

// TestProtectedSubpathCannotBeBypassedByInternalLink verifies that a link inside
// a writable root pointing at a protected subpath does not make a write to that
// subpath look writable. The lexical spelling (<root>/alias/id_rsa) is harmless,
// but the real destination (<root>/.ssh/id_rsa) must still be denied.
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

// TestOrdinaryInternalLinkStaysWritable guards against over-blocking: a link to
// an ordinary subdirectory must remain writable, because its real location does
// not fall under any protected name.
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
