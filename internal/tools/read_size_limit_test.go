package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReadFileLimitedRejectsOversize 验证尺寸预检在读入前就拒绝超大文件，
// 并返回带哨兵错误的类型化错误（含实际大小与上限，便于模型换用分页工具）。
func TestReadFileLimitedRejectsOversize(t *testing.T) {
	p := filepath.Join(t.TempDir(), "big.txt")
	if err := os.WriteFile(p, []byte(strings.Repeat("x", 1024)), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := readFileLimited(p, 512)
	if !errors.Is(err, errReadTooLarge) {
		t.Fatalf("err = %v, want errors.Is(err, errReadTooLarge)", err)
	}
	if !strings.Contains(err.Error(), "1024 bytes > 512 limit") {
		t.Fatalf("err = %q, want it to report size and limit", err)
	}
}

// TestReadFileLimitedAllowsAtLimit 验证恰好等于上限的文件仍可读（只拒绝"超过"）。
func TestReadFileLimitedAllowsAtLimit(t *testing.T) {
	p := filepath.Join(t.TempDir(), "exact.txt")
	content := []byte(strings.Repeat("y", 64))
	if err := os.WriteFile(p, content, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readFileLimited(p, int64(len(content)))
	if err != nil {
		t.Fatalf("readFileLimited at limit: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("content mismatch: got %d bytes, want %d", len(got), len(content))
	}
}

// TestReadToolRejectsOversizeFile 验证接线：read 工具对超大文件返回类型化错误，
// 而不是把几十 MB 读进内存。用真实 10 MB+ 文件触发默认上限。
func TestReadToolRejectsOversizeFile(t *testing.T) {
	dir := t.TempDir()
	payload := strings.Repeat("z", readMaxBytes+1)
	if err := os.WriteFile(filepath.Join(dir, "huge.txt"), []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}
	args, err := json.Marshal(map[string]string{"path": "huge.txt"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = (readTool{}).Run(context.Background(), dir, args)
	if !errors.Is(err, errReadTooLarge) {
		t.Fatalf("err = %v, want errors.Is(err, errReadTooLarge)", err)
	}
}
