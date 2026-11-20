package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func readWriteFileTest(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable on this host: %v", err)
	}
}

func TestWriteFileNoFollowWritesAndTruncatesRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "export.txt")
	if err := writeFileNoFollow(path, []byte("first"), 0o600); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if got := readWriteFileTest(t, path); got != "first" {
		t.Fatalf("content = %q, want %q", got, "first")
	}
	if err := writeFileNoFollow(path, []byte("ab"), 0o600); err != nil {
		t.Fatalf("second write: %v", err)
	}
	if got := readWriteFileTest(t, path); got != "ab" {
		t.Fatalf("content after truncate = %q, want %q", got, "ab")
	}
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("target became a symlink")
	}
}

func TestWriteFileNoFollowRejectsExistingSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "victim.txt")
	if err := os.WriteFile(target, []byte("do-not-touch"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "export.txt")
	symlinkOrSkip(t, target, link)

	err := writeFileNoFollow(link, []byte("pwned"), 0o600)
	if err == nil {
		t.Fatal("expected symlink rejection")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("error = %q, want a symlink mention", err)
	}
	if got := readWriteFileTest(t, target); got != "do-not-touch" {
		t.Fatalf("victim content = %q, want unchanged", got)
	}
}

func TestWriteFileNoFollowRejectsDirectoryLink(t *testing.T) {
	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(realDir, link); err != nil {
		if runtime.GOOS != "windows" {
			t.Fatal(err)
		}
		// Junctions do not require the symlink privilege on Windows and show
		// up as os.ModeIrregular, which the helper must also reject.
		out, jerr := exec.Command("cmd", "/c", "mklink", "/J", link, realDir).CombinedOutput()
		if jerr != nil {
			t.Skipf("cannot create a directory link: %v: %s", jerr, out)
		}
	}
	if err := writeFileNoFollow(link, []byte("x"), 0o600); err == nil {
		t.Fatal("expected directory link rejection")
	} else if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("error = %q, want a symlink mention", err)
	}
}

func TestWriteFileNoFollowAppliesPerm(t *testing.T) {
	path := filepath.Join(t.TempDir(), "perm.txt")
	if err := writeFileNoFollow(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		// Windows does not honour Unix mode bits; a writable file is enough.
		if fi.Mode().Perm()&0o200 == 0 {
			t.Fatalf("perm = %o, want writable", fi.Mode().Perm())
		}
		return
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Fatalf("perm = %o, want 600", got)
	}
}

func TestWriteFileNoFollowCJKPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "会话导出-中文.txt")
	content := "你好，世界 — 会话内容"
	if err := writeFileNoFollow(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readWriteFileTest(t, path); got != content {
		t.Fatalf("content = %q, want %q", got, content)
	}
}

func TestSessionsExportRefusesSymlinkOutput(t *testing.T) {
	storage := t.TempDir()
	writeJSONLSession(t, storage, "s1", []map[string]any{
		record("n1", "", "2026-01-01T00:00:00Z", "header", map[string]any{"title": "demo"}),
	})
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim.txt")
	if err := os.WriteFile(victim, []byte("sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "export.html")
	symlinkOrSkip(t, victim, out)

	stdio, _, errOut := newTestIO("", nil)
	code := RunWith(App{IO: stdio, StorageDir: storage}, []string{"sessions", "export", "s1", "-o", out})
	if code != 1 {
		t.Fatalf("exit = %d, want 1; err %q", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "symlink") {
		t.Fatalf("err = %q, want a symlink mention", errOut.String())
	}
	if got := readWriteFileTest(t, victim); got != "sentinel" {
		t.Fatalf("victim content = %q, want unchanged", got)
	}
}

func TestBugRefusesSymlinkOutput(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim.txt")
	if err := os.WriteFile(victim, []byte("sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "bug.txt")
	symlinkOrSkip(t, victim, out)

	stdio, _, errOut := newTestIO("", nil)
	code := RunWith(App{IO: stdio, StorageDir: t.TempDir(), Version: "test"}, []string{"bug", "--out", out})
	if code != 1 {
		t.Fatalf("exit = %d, want 1; err %q", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "symlink") {
		t.Fatalf("err = %q, want a symlink mention", errOut.String())
	}
	if got := readWriteFileTest(t, victim); got != "sentinel" {
		t.Fatalf("victim content = %q, want unchanged", got)
	}
}
