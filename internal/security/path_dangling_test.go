package security

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestDanglingSymlinkCannotEscapeWriteRoots covers a link whose target does not
// exist yet. os.Open reports it as "missing", so the lexical tail-reattachment
// path used to keep the link's own name and let a write follow it outside the
// workspace. The real target must be resolved even before it exists.
func TestDanglingSymlinkCannotEscapeWriteRoots(t *testing.T) {
	requireSymlinks(t)
	cases := []struct {
		name       string
		linkTarget func(root, outside string) string
		wantInside bool
	}{
		{
			name: "dangling final link to absolute outside path",
			linkTarget: func(_, outside string) string {
				return filepath.Join(outside, "pwned.txt")
			},
		},
		{
			name: "dangling final link to relative outside path",
			linkTarget: func(root, _ string) string {
				return filepath.Join("..", "outside", "pwned.txt")
			},
		},
		{
			name: "dangling link inside the root is still writable",
			linkTarget: func(root, _ string) string {
				return filepath.Join(root, "inside.txt")
			},
			wantInside: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, "root")
			outside := filepath.Join(base, "outside")
			for _, dir := range []string{root, outside} {
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			link := filepath.Join(root, "outlink")
			symlinkTarget := tc.linkTarget(root, outside)
			if err := os.Symlink(symlinkTarget, link); err != nil {
				t.Fatal(err)
			}

			// The link resolves to its target, so the expected real path is the
			// link target itself (made absolute for the relative-target case).
			// The in-root case must not be compared with the outside escape path.
			want := symlinkTarget
			if !filepath.IsAbs(want) {
				want = filepath.Join(root, want)
			}
			assertDanglingLinkBoundary(t, root, link, want, tc.wantInside)
		})
	}
}

// TestDanglingSymlinkInIntermediateComponentCannotEscape covers a dangling
// link in the middle of a not-yet-created tail. The boundary must fail closed
// even though no ancestor of the final path exists on disk.
func TestDanglingSymlinkInIntermediateComponentCannotEscape(t *testing.T) {
	requireSymlinks(t)
	base := t.TempDir()
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	for _, dir := range []string{root, outside} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(root, "linkdir")
	if err := os.Symlink(filepath.Join(outside, "missing-dir"), link); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(outside, "missing-dir", "file.txt")
	path := filepath.Join(link, "file.txt")

	resolved, err := ResolveRealPath(path)
	if err != nil {
		t.Fatalf("ResolveRealPath: %v", err)
	}
	// On Windows t.TempDir() may yield an 8.3 short path while ResolveRealPath
	// returns the long form; resolve the expected path through the same helper
	// so both sides are spelled identically.
	want, err := ResolveRealPath(target)
	if err != nil {
		t.Fatalf("ResolveRealPath(target): %v", err)
	}
	if !samePath(resolved, want) {
		t.Fatalf("ResolveRealPath = %q, want %q", resolved, want)
	}
	if err := InWriteRoots(filepath.Join("linkdir", "file.txt"), []string{root}); err == nil {
		t.Fatal("a write through a dangling intermediate symlink must be denied")
	}
}

// TestDanglingJunctionCannotEscapeWriteRoots is the Windows counterpart:
// junctions need no symlink privilege, so this runs where os.Symlink is
// unavailable and proves the reparse-point path is covered too.
func TestDanglingJunctionCannotEscapeWriteRoots(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("junctions are Windows-only")
	}
	cases := []struct {
		name   string
		inside bool
	}{
		{name: "dangling junction to an outside file"},
		{name: "dangling junction inside the root stays writable", inside: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, "root")
			outside := filepath.Join(base, "outside")
			for _, dir := range []string{root, outside} {
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			targetFile := filepath.Join(outside, "pwned.txt")
			if tc.inside {
				targetFile = filepath.Join(root, "inside.txt")
			}
			link := filepath.Join(root, "outlink")
			out, err := exec.Command("cmd", "/C", "mklink", "/J", link, targetFile).CombinedOutput()
			if err != nil {
				t.Skipf("junction creation unavailable: %v (%s)", err, strings.TrimSpace(string(out)))
			}

			assertDanglingLinkBoundary(t, root, link, targetFile, tc.inside)
		})
	}
}

// assertDanglingLinkBoundary checks the shared contract: ResolveRealPath sees
// the link's real target, and the write boundary accepts only targets that
// actually lie inside root.
func assertDanglingLinkBoundary(t *testing.T, root, link, target string, wantInside bool) {
	t.Helper()
	resolved, err := ResolveRealPath(link)
	if err != nil {
		t.Fatalf("ResolveRealPath(%q): %v", link, err)
	}
	// Compare against the resolved target, not the raw lexical path: on Windows
	// the temp dir may contain 8.3 short-name components while ResolveRealPath
	// returns the canonical long form, and EqualFold cannot bridge the two.
	wantResolved, err := ResolveRealPath(target)
	if err != nil {
		t.Fatalf("ResolveRealPath(target %q): %v", target, err)
	}
	if !samePath(resolved, wantResolved) {
		t.Fatalf("ResolveRealPath = %q, want %q", resolved, wantResolved)
	}

	err = InWriteRoots(filepath.Base(link), []string{root})
	if wantInside && err != nil {
		t.Fatalf("in-root dangling link must stay writable: %v", err)
	}
	if !wantInside && err == nil {
		t.Fatal("a dangling link pointing outside the roots must be denied")
	}

	decision := DefaultPolicy(root).Check("write", []byte(fmt.Sprintf(`{"path":%q}`, link)))
	want := Deny
	if wantInside {
		want = Ask
	}
	if decision != want {
		t.Fatalf("policy write decision = %v, want %v", decision, want)
	}
}

func samePath(a, b string) bool {
	if a == b {
		return true
	}
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}
