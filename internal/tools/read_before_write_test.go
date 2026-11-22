package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReadBeforeWriteDetectsExternalChange(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "note.txt")
	if err := os.WriteFile(path, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := NewFileFreshness()
	ctx := WithFileFreshness(context.Background(), f)

	// 读：登记快照。
	readRaw, _ := json.Marshal(map[string]any{"path": "note.txt"})
	if _, err := (readTool{}).Run(ctx, root, readRaw); err != nil {
		t.Fatal(err)
	}
	if f.IsStale(path) {
		t.Fatal("freshly read file should not be stale")
	}

	// 外部改动：模拟另一个 agent 写入。
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(path, []byte("changed by someone else"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !f.IsStale(path) {
		t.Fatal("external change should mark file stale")
	}

	// 编辑：应被拒绝，磁盘内容不变。
	editRaw, _ := json.Marshal(map[string]any{"path": "note.txt", "old": "original", "new": "mine"})
	if _, err := (editTool{}).Run(ctx, root, editRaw); err == nil {
		t.Fatal("edit should be rejected on a stale file")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "changed by someone else" {
		t.Fatalf("stale edit modified the file: %q", data)
	}
}

func TestReadBeforeWriteAllowsAfterReread(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "note.txt")
	if err := os.WriteFile(path, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := NewFileFreshness()
	ctx := WithFileFreshness(context.Background(), f)
	readRaw, _ := json.Marshal(map[string]any{"path": "note.txt"})

	_, _ = (readTool{}).Run(ctx, root, readRaw)
	time.Sleep(10 * time.Millisecond)
	_ = os.WriteFile(path, []byte("changed"), 0o644)
	_, _ = (readTool{}).Run(ctx, root, readRaw) // 重读刷新快照
	if f.IsStale(path) {
		t.Fatal("reread should refresh snapshot")
	}
	editRaw, _ := json.Marshal(map[string]any{"path": "note.txt", "old": "changed", "new": "mine"})
	if _, err := (editTool{}).Run(ctx, root, editRaw); err != nil {
		t.Fatalf("edit after reread should succeed: %v", err)
	}
}

func TestReadBeforeWriteNoLedgerIsNoop(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 未注入账本时 read/edit 必须照常工作。
	readRaw, _ := json.Marshal(map[string]any{"path": "a.txt"})
	if _, err := (readTool{}).Run(context.Background(), root, readRaw); err != nil {
		t.Fatal(err)
	}
	editRaw, _ := json.Marshal(map[string]any{"path": "a.txt", "old": "x", "new": "y"})
	if _, err := (editTool{}).Run(context.Background(), root, editRaw); err != nil {
		t.Fatal(err)
	}
}
