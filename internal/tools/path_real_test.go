package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "home")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}

	if _, err := resolve(root, filepath.Join("home", "id_rsa")); err == nil {
		t.Fatal("resolve must reject a path that escapes through a symlink")
	}
	if _, err := resolve(root, "normal.txt"); err != nil {
		t.Fatalf("an ordinary relative path must stay resolvable: %v", err)
	}
}
