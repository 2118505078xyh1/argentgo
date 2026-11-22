package tools

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTrimSpillArtifactsByTotalBytes(t *testing.T) {
	dir := t.TempDir()
	// 造 10 个符合契约的产出，每个 100 字节，mtime 依次递增。
	names := make([]string, 10)
	base := time.Now().Add(-time.Hour)
	for i := 0; i < 10; i++ {
		// 契约：tool-output-<16 位 hex>.log
		name := "tool-output-000000000000000" + string(rune('0'+i)) + ".log"
		names[i] = name
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, make([]byte, 100), 0o600); err != nil {
			t.Fatal(err)
		}
		ts := base.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(path, ts, ts); err != nil {
			t.Fatal(err)
		}
	}
	// 上限 450 字节 → 应删除最旧的 6 个，保留 4 个（400 字节）。
	removed, freed, err := TrimSpillArtifacts(dir, 450)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 6 || freed != 600 {
		t.Fatalf("removed=%d freed=%d, want 6/600", removed, freed)
	}
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, names[i])); !os.IsNotExist(err) {
			t.Errorf("oldest %s should be removed", names[i])
		}
	}
	for i := 6; i < 10; i++ {
		if _, err := os.Stat(filepath.Join(dir, names[i])); err != nil {
			t.Errorf("newer %s should be kept: %v", names[i], err)
		}
	}
}

func TestTrimSpillArtifactsDisabledAndNoop(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tool-output-00000000000000aa.log"), make([]byte, 100), 0o600); err != nil {
		t.Fatal(err)
	}
	// maxBytes<=0 禁用。
	if removed, _, _ := TrimSpillArtifacts(dir, 0); removed != 0 {
		t.Fatal("maxBytes=0 should disable trimming")
	}
	// 总量未超限 → 空操作。
	if removed, _, _ := TrimSpillArtifacts(dir, 1000); removed != 0 {
		t.Fatal("under limit should be no-op")
	}
	// 目录不存在 → 空操作无错误。
	if removed, _, err := TrimSpillArtifacts(filepath.Join(dir, "missing"), 10); removed != 0 || err != nil {
		t.Fatalf("missing dir = %d/%v", removed, err)
	}
}

func TestTrimSpillArtifactsIgnoresNonArtifacts(t *testing.T) {
	dir := t.TempDir()
	// 无关文件不应被删除（即使超限）。
	other := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(other, make([]byte, 1000), 0o600); err != nil {
		t.Fatal(err)
	}
	removed, _, _ := TrimSpillArtifacts(dir, 1)
	if removed != 0 {
		t.Fatal("non-artifact file must not be trimmed")
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("non-artifact file was touched: %v", err)
	}
}
