package update

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func stage(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile %s: %v", path, err)
	}
	return string(data)
}

func TestApplyReplacesTheBinary(t *testing.T) {
	dir := t.TempDir()
	target := stage(t, dir, "bin/argent.exe", "old build")
	dest := stage(t, dir, "downloads/argent.exe", "new build")

	if err := Apply(dest, target); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := readFile(t, target); got != "new build" {
		t.Fatalf("target = %q, want the new build", got)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("staged file still exists: %v", err)
	}

	stale := target + StaleSuffix
	if runtime.GOOS == "windows" {
		// The previous binary is kept next to the new one; a running exe cannot
		// be deleted, only renamed.
		if got := readFile(t, stale); got != "old build" {
			t.Fatalf("%s = %q, want the old build", stale, got)
		}
	} else {
		if _, err := os.Stat(stale); !os.IsNotExist(err) {
			t.Fatalf("stale backup left on unix: %v", err)
		}
	}
}

func TestApplyDropsAStaleBackupFirst(t *testing.T) {
	dir := t.TempDir()
	target := stage(t, dir, "argent.exe", "old build")
	stage(t, dir, "argent.exe"+StaleSuffix, "junk from a crashed update")
	dest := stage(t, dir, "download/argent.exe", "new build")

	if err := Apply(dest, target); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := readFile(t, target); got != "new build" {
		t.Fatalf("target = %q", got)
	}
	// Whatever remains at .old must be the previous build, never the stale junk.
	if data, err := os.ReadFile(target + StaleSuffix); err == nil && string(data) == "junk from a crashed update" {
		t.Fatal("the stale .old file survived the update")
	}
}

func TestApplyCreatesAMissingTarget(t *testing.T) {
	dir := t.TempDir()
	dest := stage(t, dir, "download/argent.exe", "first install")
	target := filepath.Join(dir, "install", "nested", "argent.exe")

	if err := Apply(dest, target); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := readFile(t, target); got != "first install" {
		t.Fatalf("target = %q", got)
	}
}

func TestApplyNeedsAStagedFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "argent.exe")
	if err := Apply(filepath.Join(dir, "missing"), target); err == nil {
		t.Fatal("Apply accepted a missing staged file")
	} else if !strings.Contains(err.Error(), "staged") {
		t.Fatalf("error = %v", err)
	}
}

func TestApplyRejectsDirectoriesAndEmptyArguments(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "argent.exe")
	if err := Apply("", target); err == nil {
		t.Fatal("Apply accepted an empty source")
	}
	if err := Apply(stage(t, dir, "src", "x"), ""); err == nil {
		t.Fatal("Apply accepted an empty target")
	}
	if err := Apply(dir, target); err == nil {
		t.Fatal("Apply accepted a directory as the source")
	}
}

func TestApplyKeepsTheTargetOnAFailedSwap(t *testing.T) {
	dir := t.TempDir()
	target := stage(t, dir, "argent.exe", "old build")
	dest := stage(t, dir, "download/argent.exe", "new build")
	// Making the install directory read-only would not be portable (Windows
	// ignores the mode), so this test only covers the missing-source guard: the
	// target must still be intact afterwards.
	if err := Apply(filepath.Join(dir, "gone"), target); err == nil {
		t.Fatal("Apply accepted a missing source")
	}
	if got := readFile(t, target); got != "old build" {
		t.Fatalf("target changed to %q after a failed apply", got)
	}
	if got := readFile(t, dest); got != "new build" {
		t.Fatalf("staged file changed to %q", got)
	}
}
