package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

// TestNarrowVerifyArgvByChangedFiles pins the core optimisation: a full-repo
// `go test ./...` target is replaced by the deduplicated, sorted set of changed
// Go packages, and the configured argv slice is left untouched.
func TestNarrowVerifyArgvByChangedFiles(t *testing.T) {
	root := t.TempDir()
	argv := []string{"go", "test", "./..."}
	got, narrowed, skipped := narrowVerifyArgv(argv,
		[]string{"internal/llm/bar.go", "internal/app/foo.go", "internal/app/baz.go"}, root)
	if !narrowed {
		t.Fatal("narrowed = false, want true")
	}
	if skipped != 0 {
		t.Fatalf("skipped = %d, want 0", skipped)
	}
	want := []string{"go", "test", "./internal/app", "./internal/llm"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %q, want %q", got, want)
	}
	if !reflect.DeepEqual(argv, []string{"go", "test", "./..."}) {
		t.Fatalf("configured argv was mutated: %q", argv)
	}
}

// TestVerifyNarrowTargetsRejectsUnsafeAndOutOfWorkspacePaths pins the security
// boundary: traversal, absolute/drive/UNC paths, shell metacharacters,
// whitespace, NUL and empty inputs are all skipped rather than interpolated.
func TestVerifyNarrowTargetsRejectsUnsafeAndOutOfWorkspacePaths(t *testing.T) {
	root := t.TempDir()
	unsafe := []string{
		"../escape.go",                // lexical traversal
		"a/../../escape.go",           // traversal after clean
		`..\evil.go`,                  // windows-style traversal
		"internal/app/..\\secret.go",  // traversal through a mixed separator
		"/etc/passwd",                 // absolute unix
		`C:\Windows\system32\evil.go`, // windows drive prefix
		`\\server\share\evil.go`,      // UNC root
		"internal/app/x.go; rm -rf /", // shell separator
		"internal/app/$(whoami).go",   // command substitution
		"internal/app/a.go|cat",       // pipe
		"internal/app/a b.go",         // whitespace
		"internal/app/a.go\x00.go",    // embedded NUL
		"",                            // empty
	}
	targets, skipped := verifyNarrowTargets(root, unsafe)
	if len(targets) != 0 {
		t.Fatalf("targets = %q, want none", targets)
	}
	if skipped != len(unsafe) {
		t.Fatalf("skipped = %d, want %d", skipped, len(unsafe))
	}

	// Even with one safe path alongside the unsafe ones, only the safe one is
	// used and the bad paths are counted as skipped.
	targets, skipped = verifyNarrowTargets(root, append([]string{"ok/ok.go"}, unsafe...))
	if !reflect.DeepEqual(targets, []string{"./ok"}) {
		t.Fatalf("targets = %q, want [./ok]", targets)
	}
	if skipped != len(unsafe) {
		t.Fatalf("skipped = %d, want %d", skipped, len(unsafe))
	}
}

// TestNarrowVerifyArgvNoPathsKeepsFullCommand pins the unchanged behaviour when
// no changed paths are available: the configured full command runs verbatim.
func TestNarrowVerifyArgvNoPathsKeepsFullCommand(t *testing.T) {
	for _, argv := range [][]string{
		{"go", "test", "./..."},
		{"go", "build", "./..."},
		{"go", "test"},
		{"./tools/check.exe", "--quiet"},
	} {
		got, narrowed, skipped := narrowVerifyArgv(argv, nil, t.TempDir())
		if narrowed || skipped != 0 {
			t.Fatalf("argv %q: narrowed=%v skipped=%d, want false/0", argv, narrowed, skipped)
		}
		if !reflect.DeepEqual(got, argv) {
			t.Fatalf("argv %q: got %q, want unchanged", argv, got)
		}
	}
}

// TestVerifyNarrowTargetsCJKPaths pins that Unicode (CJK) path components are
// accepted by the character allowlist and survive as a valid package pattern,
// while a non-Go file in the same batch is still skipped.
func TestVerifyNarrowTargetsCJKPaths(t *testing.T) {
	root := t.TempDir()
	paths := []string{"内部/模块/工具.go", "内部/模块/另一个.go", "docs/说明.md"}
	targets, skipped := verifyNarrowTargets(root, paths)
	if !reflect.DeepEqual(targets, []string{"./内部/模块"}) {
		t.Fatalf("targets = %q, want [./内部/模块]", targets)
	}
	if skipped != 1 {
		t.Fatalf("skipped = %d, want 1 (the .md file)", skipped)
	}

	got, narrowed, _ := narrowVerifyArgv([]string{"go", "test", "./..."}, paths, root)
	if !narrowed {
		t.Fatal("narrowed = false, want true")
	}
	want := []string{"go", "test", "./内部/模块"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %q, want %q", got, want)
	}
}

// TestNarrowVerifyArgvModesAndNoOps pins the two narrowing modes and every case
// where narrowing must be refused: explicit operator targets, unknown command
// shapes and .exe commands keep their exact configured argv.
func TestNarrowVerifyArgvModesAndNoOps(t *testing.T) {
	root := t.TempDir()
	paths := []string{"a/a.go"}
	tests := []struct {
		name     string
		argv     []string
		want     []string
		narrowed bool
	}{
		{
			name:     "append when no target declared",
			argv:     []string{"go", "test"},
			want:     []string{"go", "test", "./a"},
			narrowed: true,
		},
		{
			name:     "replace wildcard and keep flags",
			argv:     []string{"go", "test", "-race", "./..."},
			want:     []string{"go", "test", "-race", "./a"},
			narrowed: true,
		},
		{
			name:     "operator explicit target is not overridden",
			argv:     []string{"go", "test", "./pkg/..."},
			want:     []string{"go", "test", "./pkg/..."},
			narrowed: false,
		},
		{
			name:     "flag with separate value is not overridden",
			argv:     []string{"go", "test", "-count", "1"},
			want:     []string{"go", "test", "-count", "1"},
			narrowed: false,
		},
		{
			name:     "exe command is never narrowed",
			argv:     []string{"./check.exe", "--quiet"},
			want:     []string{"./check.exe", "--quiet"},
			narrowed: false,
		},
		{
			name:     "go run is not a verification target",
			argv:     []string{"go", "run", "."},
			want:     []string{"go", "run", "."},
			narrowed: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, narrowed, _ := narrowVerifyArgv(tc.argv, paths, root)
			if narrowed != tc.narrowed {
				t.Fatalf("narrowed = %v, want %v", narrowed, tc.narrowed)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("argv = %q, want %q", got, tc.want)
			}
		})
	}
}

// writeNarrowProbeModule creates a tiny module with one passing package (a) and
// one failing package (b). A full `go test ./...` fails; a narrowed
// `go test ./a` passes, which lets the tests observe narrowing end to end.
func writeNarrowProbeModule(t *testing.T, workspace string) {
	t.Helper()
	writeVerifyFile(t, workspace, "go.mod", "module narrowprobe\n\ngo 1.21\n")
	if err := os.MkdirAll(filepath.Join(workspace, "a"), 0o755); err != nil {
		t.Fatalf("mkdir a: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, "b"), 0o755); err != nil {
		t.Fatalf("mkdir b: %v", err)
	}
	writeVerifyFile(t, filepath.Join(workspace, "a"), "a_test.go",
		"package a\n\nimport \"testing\"\n\nfunc TestOK(t *testing.T) {}\n")
	writeVerifyFile(t, filepath.Join(workspace, "b"), "b_test.go",
		"package b\n\nimport \"testing\"\n\nfunc TestFail(t *testing.T) { t.Fatal(\"BOOM_B\") }\n")
}

// TestNewVerifyFuncNarrowsGoTestToChangedPackage is the end-to-end proof: with a
// changed path in package a the runner passes (only ./a is tested); with no
// paths it falls back to ./... and sees the deliberately failing package b.
func TestNewVerifyFuncNarrowsGoTestToChangedPackage(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("go toolchain not on PATH: %v", err)
	}
	workspace := t.TempDir()
	writeNarrowProbeModule(t, workspace)
	verify := NewVerifyFunc([]string{"go", "test", "./..."}, 120000, workspace)

	_, passed, _, ok := verify(context.Background(), []string{"a/a.go"})
	if !ok || !passed {
		t.Fatalf("narrowed run = (passed=%v, ok=%v), want a passing verdict", passed, ok)
	}
	_, passed, _, ok = verify(context.Background(), nil)
	if !ok || passed {
		t.Fatalf("full run = (passed=%v, ok=%v), want a failing verdict", passed, ok)
	}
}

// TestVerifyDispatchForwardsChangedPaths guards the dispatcher wiring: the
// per-language runner must receive the checkpoint paths, not its own command
// argv. With the wildcard command below, a forwarded argv would be mistaken for
// package names (./go, ./test) and the check would fail spuriously.
func TestVerifyDispatchForwardsChangedPaths(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("go toolchain not on PATH: %v", err)
	}
	workspace := t.TempDir()
	writeNarrowProbeModule(t, workspace)
	dispatch := newVerifyDispatchFunc(map[string]string{"*": "go test ./..."}, nil, 120000, workspace)

	_, passed, _, ok := dispatch(context.Background(), []string{"a/a.go"})
	if !ok || !passed {
		t.Fatalf("dispatch narrowed run = (passed=%v, ok=%v), want a passing verdict", passed, ok)
	}
	_, passed, _, ok = dispatch(context.Background(), nil)
	if !ok || passed {
		t.Fatalf("dispatch full run = (passed=%v, ok=%v), want a failing verdict", passed, ok)
	}
}
