//go:build windows

package tools

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveRejectsJunctionEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "home")
	out, err := exec.Command("cmd", "/C", "mklink", "/J", link, outside).CombinedOutput()
	if err != nil {
		t.Skipf("junction creation is unavailable: %v (%s)", err, strings.TrimSpace(string(out)))
	}

	if _, err := resolve(root, filepath.Join("home", "id_rsa")); err == nil {
		t.Fatal("resolve must reject a path that escapes through a junction")
	}
	if _, err := resolve(root, "normal.txt"); err != nil {
		t.Fatalf("an ordinary relative path must stay resolvable: %v", err)
	}
}
