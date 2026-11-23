package bash

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBashReadFileLimitedRejectsOversize 验证尺寸预检在读入前就拒绝超大文件。
// 用 Truncate 造稀疏文件，不真写满 10MB，避免测试占用磁盘。
func TestBashReadFileLimitedRejectsOversize(t *testing.T) {
	p := filepath.Join(t.TempDir(), "big.txt")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(bashReadMaxBytes + 1); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()

	_, err = readFileLimited(p, bashReadMaxBytes)
	if !errors.Is(err, errBashFileTooLarge) {
		t.Fatalf("err = %v, want errors.Is(err, errBashFileTooLarge)", err)
	}
	// 替代命令提示必须出现在错误文本里（跨包后哨兵身份丢失，只剩文本）。
	if !strings.Contains(err.Error(), "sed -n") {
		t.Fatalf("err = %q, want paging hint", err)
	}
}

// TestBashReadFileLimitedAllowsAtLimit 验证等于上限的文件仍可读。
func TestBashReadFileLimitedAllowsAtLimit(t *testing.T) {
	p := filepath.Join(t.TempDir(), "exact.txt")
	if err := os.WriteFile(p, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readFileLimited(p, 5)
	if err != nil {
		t.Fatalf("readFileLimited at limit: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("content = %q, want hello", got)
	}
}

// TestGzipFileToFileStreams 验证流式文件压缩/解压的往返正确性（不整文件读入）。
func TestGzipFileToFileStreams(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "data.txt")
	payload := strings.Repeat("hello gzip world\n", 2048) // ~32KB，跨多个压缩块
	if err := os.WriteFile(src, []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}
	gz := filepath.Join(dir, "data.txt.gz")
	if err := gzipFileToFile(src, gz, false); err != nil {
		t.Fatalf("gzip: %v", err)
	}
	// 确认产物确实是 gzip 格式（魔数）。
	head, err := os.ReadFile(gz)
	if err != nil || len(head) < 2 || head[0] != 0x1f || head[1] != 0x8b {
		t.Fatalf("not a gzip file: head=%x err=%v", head, err)
	}
	out := filepath.Join(dir, "restored.txt")
	if err := gzipFileToFile(gz, out, true); err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != payload {
		t.Fatalf("round trip mismatch: got %d bytes, want %d", len(got), len(payload))
	}
}
