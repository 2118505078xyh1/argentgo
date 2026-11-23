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

// mkSparseFile 造一个逻辑大小为 size 的稀疏文件（不真写满磁盘）。
func mkSparseFile(t *testing.T, dir, name string, size int64) string {
	t.Helper()
	p := filepath.Join(dir, name)
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(size); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()
	return p
}

// TestContextPackRejectsOversize 验证 context_pack 对超过小文件预算的文件
// 在读入前拒绝，而不是把整文件读进内存（输出本就受 clip 约束）。
func TestContextPackRejectsOversize(t *testing.T) {
	dir := t.TempDir()
	p := mkSparseFile(t, dir, "huge.txt", int64(smallFileMaxBytes)+1)
	_, err := contextPackRead(p)
	if !errors.Is(err, errReadTooLarge) {
		t.Fatalf("err = %v, want errReadTooLarge", err)
	}
}

// TestDiffRejectsOversize 验证 diff 工具对超大输入文件返回类型化错误。
func TestDiffRejectsOversize(t *testing.T) {
	dir := t.TempDir()
	mkSparseFile(t, dir, "huge.txt", int64(smallFileMaxBytes)+1)
	if err := os.WriteFile(filepath.Join(dir, "small.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]string{"path_a": "huge.txt", "path_b": "small.txt"})
	_, err := (diffTool{}).Run(context.Background(), dir, args)
	if !errors.Is(err, errReadTooLarge) {
		t.Fatalf("err = %v, want errReadTooLarge", err)
	}
	if !strings.Contains(err.Error(), "path_a") {
		t.Fatalf("err = %q, want path_a context", err)
	}
}

// TestWriteRejectsOversizeExisting 验证 write 工具遇到超大既有文件时明确拒绝，
// 而不是盲目覆盖（没读到内容就 write 会摧毁原文件）。
func TestWriteRejectsOversizeExisting(t *testing.T) {
	dir := t.TempDir()
	mkSparseFile(t, dir, "huge.txt", int64(readMaxBytes)+1)
	args, _ := json.Marshal(map[string]string{"path": "huge.txt", "content": "new"})
	_, err := (writeTool{}).Run(context.Background(), dir, args)
	if !errors.Is(err, errReadTooLarge) {
		t.Fatalf("err = %v, want errReadTooLarge", err)
	}
	// 原文件必须未被触碰（逻辑大小仍在）。
	info, statErr := os.Stat(filepath.Join(dir, "huge.txt"))
	if statErr != nil || info.Size() != int64(readMaxBytes)+1 {
		t.Fatalf("oversize file was clobbered: info=%v err=%v", info, statErr)
	}
}
