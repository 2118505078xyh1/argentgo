package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// writeWalkTree 构造一棵含嘈杂目录的测试树。
func writeWalkTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{
		"a.go", "b.go", "src/c.go", "src/deep/d.go",
		".git/config", "node_modules/pkg/index.js", "dist/out.js",
	} {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestWalkFilesVisitsRegularFilesAndSkipsNoisyDirs(t *testing.T) {
	resetDirCache()
	root := writeWalkTree(t)
	var mu sync.Mutex
	var seen []string
	walkFiles(context.Background(), root, func(p string) bool {
		rel, err := filepath.Rel(root, p)
		if err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		seen = append(seen, filepath.ToSlash(rel))
		mu.Unlock()
		return true
	})
	joined := strings.Join(seen, "|")
	for _, want := range []string{"a.go", "b.go", "src/c.go", "src/deep/d.go"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("遍历漏掉 %q：%q", want, seen)
		}
	}
	for _, skip := range []string{".git", "node_modules", "dist"} {
		if strings.Contains(joined, skip) {
			t.Fatalf("遍历未跳过 %q：%q", skip, seen)
		}
	}
	if len(seen) != 4 {
		t.Fatalf("遍历文件数 = %d，期望 4：%q", len(seen), seen)
	}
}

func TestWalkFilesSingleFileRoot(t *testing.T) {
	resetDirCache()
	root := writeWalkTree(t)
	file := filepath.Join(root, "a.go")
	var seen []string
	walkFiles(context.Background(), file, func(p string) bool {
		seen = append(seen, p)
		return true
	})
	if len(seen) != 1 || seen[0] != file {
		t.Fatalf("单文件根遍历 = %q", seen)
	}
}

func TestWalkFilesStopsEarly(t *testing.T) {
	resetDirCache()
	root := writeWalkTree(t)
	calls := 0
	var mu sync.Mutex
	walkFiles(context.Background(), root, func(string) bool {
		mu.Lock()
		calls++
		mu.Unlock()
		return false
	})
	if calls < 1 || calls > 4 {
		t.Fatalf("提前停止后回调次数 = %d", calls)
	}
}

func TestWalkFilesHonorsContextCancellation(t *testing.T) {
	resetDirCache()
	root := writeWalkTree(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	walkFiles(ctx, root, func(string) bool {
		called = true
		return true
	})
	if called {
		t.Fatal("已取消的 context 不应触发 visit")
	}
}

func TestWalkFilesConcurrentVisitsAreSafe(t *testing.T) {
	resetDirCache()
	root := t.TempDir()
	for i := 0; i < 200; i++ {
		name := fmt.Sprintf("f%03d.go", i)
		_ = os.WriteFile(filepath.Join(root, name), []byte("x"), 0o644)
	}
	var mu sync.Mutex
	seen := make(map[string]bool)
	walkFiles(context.Background(), root, func(p string) bool {
		mu.Lock()
		seen[p] = true
		mu.Unlock()
		return true
	})
	if len(seen) != 200 {
		t.Fatalf("并发遍历去重后文件数 = %d，期望 200", len(seen))
	}
}
