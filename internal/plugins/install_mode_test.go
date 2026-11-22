package plugins

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPreserveFileMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := preserveFileMode(path, 0o755); err != nil {
		t.Fatalf("preserveFileMode: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		// Windows 无 POSIX exec 位，文件保持不变即符合预期。
		return
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("executable bit lost: %v", info.Mode().Perm())
	}
}

func TestPreserveFileModeStripsGroupWrite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode bits not applicable")
	}
	path := filepath.Join(t.TempDir(), "evil.sh")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := preserveFileMode(path, 0o777); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm()&0o022 != 0 {
		t.Fatalf("group/other write bits not stripped: %v", info.Mode().Perm())
	}
}

func TestCopyFilePreservesMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode bits not applicable")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	if err := os.WriteFile(src, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(src, dst); err != nil {
		t.Fatalf("copyFile: %v", err)
	}
	info, _ := os.Stat(dst)
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("copyFile lost exec bit: %v", info.Mode().Perm())
	}
}
