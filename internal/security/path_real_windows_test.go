//go:build windows

package security

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// makeJunction creates a directory junction without requiring administrator
// privileges. filepath.EvalSymlinks does not follow junctions on Windows, so a
// separate test is needed for the reparse-point escape.
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

	// The helper must see through the junction, unlike EvalSymlinks.
	real, err := ResolveRealPath(filepath.Join(link, "id_rsa"))
	if err != nil {
		t.Fatalf("ResolveRealPath through a junction: %v", err)
	}
	if !strings.EqualFold(real, secret) {
		t.Fatalf("junction resolved to %q, want %q", real, secret)
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
