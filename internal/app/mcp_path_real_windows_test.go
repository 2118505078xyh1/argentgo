//go:build windows

package app

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/tools"
)

func TestRouteRejectsJunctionEscape(t *testing.T) {
	root := t.TempDir()
	extra := t.TempDir()
	link := filepath.Join(root, "home")
	out, err := exec.Command("cmd", "/C", "mklink", "/J", link, extra).CombinedOutput()
	if err != nil {
		t.Skipf("junction creation is unavailable: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	m := &multiRootTools{
		roots: []string{root, extra},
		regs:  []*tools.Registry{nil, nil},
	}

	if _, _, err := m.route("read", `{"path":"home/id_rsa"}`); err == nil {
		t.Fatal("route must reject a path that escapes through a junction")
	}
	index, _, err := m.route("read", `{"path":"normal.txt"}`)
	if err != nil || index != 0 {
		t.Fatalf("an ordinary path should stay in the primary registry: index=%d err=%v", index, err)
	}
}
