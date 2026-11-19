package security

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestFromStringAcceptsTheThreeModes(t *testing.T) {
	cases := []struct {
		in   string
		want SandboxMode
	}{
		{"read-only", SandboxReadOnly},
		{"workspace-write", SandboxWorkspaceWrite},
		{"danger-full-access", SandboxDangerFullAccess},
		{"", SandboxWorkspaceWrite},
		{"  read-only ", SandboxReadOnly},
		{"WORKSPACE-WRITE", SandboxWorkspaceWrite},
	}
	for _, tc := range cases {
		got, err := FromString(tc.in)
		if err != nil {
			t.Fatalf("%q: %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("%q: got %q, want %q", tc.in, got, tc.want)
		}
		if !got.Valid() {
			t.Fatalf("%q: mode reports invalid", tc.in)
		}
	}
	for _, bad := range []string{"sandbox", "readonly", "none", "workspace_write"} {
		if _, err := FromString(bad); err == nil {
			t.Fatalf("%q must be rejected", bad)
		}
	}
	if SandboxMode("bogus").Valid() {
		t.Fatal("unknown mode reports valid")
	}
}

func TestIsMutatingAllowedByMode(t *testing.T) {
	cases := []struct {
		mode SandboxMode
		tool string
		want bool
	}{
		{SandboxReadOnly, "read", true},
		{SandboxReadOnly, "grep", true},
		{SandboxReadOnly, "write", false},
		{SandboxReadOnly, "edit", false},
		{SandboxReadOnly, "bash", false},
		{SandboxReadOnly, "deploy", false},
		{SandboxWorkspaceWrite, "write", true},
		{SandboxWorkspaceWrite, "read", true},
		{SandboxDangerFullAccess, "bash", true},
		{SandboxDangerFullAccess, "deploy", true},
	}
	for _, tc := range cases {
		if got := tc.mode.IsMutatingAllowed(tc.tool); got != tc.want {
			t.Fatalf("%s/%s: got %v, want %v", tc.mode, tc.tool, got, tc.want)
		}
	}
}

func TestInWriteRoots(t *testing.T) {
	root := t.TempDir()
	extra := t.TempDir()
	roots := []string{root, extra}

	cases := []struct {
		name string
		path string
		ok   bool
	}{
		{"relative file", "src/main.go", true},
		{"relative dot", ".", true},
		{"dot segments", "src/./a.go", true},
		{"absolute inside", filepath.Join(root, "a", "b.txt"), true},
		{"extra root", filepath.Join(extra, "c.txt"), true},
		{"traversal escape", "../escape.txt", false},
		{"nested traversal", "src/../../escape.txt", false},
		{"absolute outside", filepath.Join(t.TempDir(), "x.txt"), false},
	}
	for _, tc := range cases {
		err := InWriteRoots(tc.path, roots)
		if tc.ok && err != nil {
			t.Fatalf("%s (%q): %v", tc.name, tc.path, err)
		}
		if !tc.ok {
			if err == nil {
				t.Fatalf("%s (%q): expected an error", tc.name, tc.path)
			}
			if !strings.Contains(err.Error(), "outside the write roots") {
				t.Fatalf("%s: unexpected error %q", tc.name, err)
			}
		}
	}
	if err := InWriteRoots("a.txt", nil); err == nil {
		t.Fatal("no roots must be an error")
	}
}

func TestWriteRootsFromDeduplicates(t *testing.T) {
	root := t.TempDir()
	roots := WriteRootsFrom(root, []string{root, filepath.Join(root, "sub"), ""})
	if len(roots) != 2 {
		t.Fatalf("got %v, want the root and one extra", roots)
	}
	if roots[0] != filepath.Clean(root) {
		t.Fatalf("first root = %q, want %q", roots[0], root)
	}
}
