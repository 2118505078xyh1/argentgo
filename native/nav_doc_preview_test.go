package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNavDocPreview(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# Title\n\nhello\nworld\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	read := func(path string) ([]byte, error) { return os.ReadFile(path) }
	rows := navDocPreview("文档", root, read)
	if len(rows) == 0 || !strings.Contains(rows[0], "README.md") {
		t.Fatalf("rows = %v", rows)
	}
	if !strings.Contains(strings.Join(rows, "|"), "hello") {
		t.Fatalf("real content not shown: %v", rows)
	}
	// 记忆面板无文件 → 回退静态行（非"敬请期待"回退面板）。
	rows = navDocPreview("记忆", root, read)
	if len(rows) == 0 {
		t.Fatal("fallback rows missing")
	}
	// 非文档面板返回 nil。
	if navDocPreview("Git", root, read) != nil {
		t.Fatal("Git panel should have no doc preview")
	}
}
