package app

import (
	"os"
	"path/filepath"
	"testing"

	"example.com/argentgo/internal/tools"
)

func TestRouteRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	extra := t.TempDir()
	link := filepath.Join(root, "home")
	if err := os.Symlink(extra, link); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}
	m := &multiRootTools{
		roots: []string{root, extra},
		regs:  []*tools.Registry{nil, nil},
	}

	if _, _, err := m.route("read", `{"path":"home/id_rsa"}`); err == nil {
		t.Fatal("route must reject a path that escapes through a symlink")
	}
	index, _, err := m.route("read", `{"path":"normal.txt"}`)
	if err != nil || index != 0 {
		t.Fatalf("an ordinary path should stay in the primary registry: index=%d err=%v", index, err)
	}
}
