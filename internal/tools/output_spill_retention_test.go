package tools

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeArtifactForTest 创建一个规范的溢出产出，并把它
// 的 mtime 回退 age，返回其路径。
func writeArtifactForTest(t *testing.T, dir, id, content string, age time.Duration) string {
	t.Helper()
	path := filepath.Join(dir, spillArtifactPrefix+id+spillArtifactSuffix)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
	stamp := time.Now().Add(-age)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatalf("Chtimes(%q): %v", path, err)
	}
	return path
}

func TestCleanSpillArtifactsRemovesExpiredOnly(t *testing.T) {
	dir := t.TempDir()
	old := writeArtifactForTest(t, dir, "0000000000000001", "old", 8*24*time.Hour)
	fresh := writeArtifactForTest(t, dir, "0000000000000002", "fresh", time.Hour)
	// 无关文件即使看起来很旧也必须保留。
	unrelated := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(unrelated, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(unrelated, stamp, stamp); err != nil {
		t.Fatal(err)
	}

	removed, err := CleanSpillArtifacts(dir, DefaultSpillRetention)
	if err != nil {
		t.Fatalf("CleanSpillArtifacts: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("expired artifact still present: stat err = %v", err)
	}
	for _, keep := range []string{fresh, unrelated} {
		if _, err := os.Stat(keep); err != nil {
			t.Fatalf("expected %q to be kept: %v", keep, err)
		}
	}
}

func TestCleanSpillArtifactsIsIdempotentAndSafe(t *testing.T) {
	dir := t.TempDir()
	old := writeArtifactForTest(t, dir, "0000000000000003", "old", 30*24*time.Hour)

	// 目录不存在是空操作，而非错误。
	if n, err := CleanSpillArtifacts(filepath.Join(dir, "absent"), DefaultSpillRetention); n != 0 || err != nil {
		t.Fatalf("missing dir: removed=%d err=%v", n, err)
	}
	// 非正的 age 会完全禁用清理。
	if n, err := CleanSpillArtifacts(dir, 0); n != 0 || err != nil {
		t.Fatalf("disabled cleanup: removed=%d err=%v", n, err)
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatalf("non-positive age removed an artifact: %v", err)
	}

	if n, err := CleanSpillArtifacts(dir, DefaultSpillRetention); n != 1 || err != nil {
		t.Fatalf("first sweep: removed=%d err=%v", n, err)
	}
	if n, err := CleanSpillArtifacts(dir, DefaultSpillRetention); n != 0 || err != nil {
		t.Fatalf("second sweep must be a no-op: removed=%d err=%v", n, err)
	}
}

func TestCleanSpillArtifactsIgnoresLookalikeNames(t *testing.T) {
	dir := t.TempDir()
	// 近似名称共享前缀/后缀，但不是规范产出；
	// 清理不得删除它们。
	for _, name := range []string{
		"tool-output-xyz.log",
		"tool-output-0000000000000001.log.tmp",
		"tool-output-00000000000000001.log",
		"a-tool-output-0000000000000001.log",
	} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		stamp := time.Now().Add(-30 * 24 * time.Hour)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := CleanSpillArtifacts(dir, DefaultSpillRetention); n != 0 || err != nil {
		t.Fatalf("lookalike names were touched: removed=%d err=%v", n, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 4 {
		t.Fatalf("lookalike files changed: entries=%d err=%v", len(entries), err)
	}
}
