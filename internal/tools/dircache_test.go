package tools

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestDirCacheSecondReadIsFree(t *testing.T) {
	resetDirCache()
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// 让目录 mtime 离开 50ms 竞态窗口。
	old := time.Now().Add(-time.Second)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatal(err)
	}

	first, err := readDirCached(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, missedAfterFirst := dirCacheCounters()
	second, err := readDirCached(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, missedAfterSecond := dirCacheCounters()
	if missedAfterSecond != missedAfterFirst {
		t.Fatalf("第二次读触发了 readdir：miss %d -> %d", missedAfterFirst, missedAfterSecond)
	}
	if len(first) != len(second) || len(second) != 1 {
		t.Fatalf("目录项 = %d / %d", len(first), len(second))
	}
}

func TestDirCacheInvalidatesOnExternalChange(t *testing.T) {
	resetDirCache()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Second)
	_ = os.Chtimes(dir, old, old)
	if _, err := readDirCached(dir); err != nil {
		t.Fatal(err)
	}

	// 外部新建文件：mtime 变化，缓存必须失效。
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readDirCached(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("外部建文件后目录项 = %d，期望 2：%+v", len(got), got)
	}
}

func TestDirCacheRacyWindowNotCached(t *testing.T) {
	resetDirCache()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 刚创建的目录 mtime 在 50ms 内，不应入缓存。
	_, _ = readDirCached(dir)
	hits, _ := dirCacheCounters()
	_, _ = readDirCached(dir)
	hits2, _ := dirCacheCounters()
	if hits2 != hits {
		t.Fatalf("竞态窗口内不应命中缓存：hits %d -> %d", hits, hits2)
	}
}

func TestDirCacheIgnoreVersionInvalidatesAll(t *testing.T) {
	resetDirCache()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Second)
	_ = os.Chtimes(dir, old, old)
	_, _ = readDirCached(dir)
	hits, _ := dirCacheCounters()
	_, _ = readDirCached(dir)
	hits2, _ := dirCacheCounters()
	if hits2 <= hits {
		t.Fatalf("前置条件不成立：缓存未命中，hits %d -> %d", hits, hits2)
	}

	invalidateDirCache()
	_, missesBefore := dirCacheCounters()
	_, _ = readDirCached(dir)
	_, missesAfter := dirCacheCounters()
	if missesAfter <= missesBefore {
		t.Fatalf("ignore 源 stamp 变化后应重新 readdir：miss %d -> %d", missesBefore, missesAfter)
	}
}

func TestDirCacheConcurrentAccess(t *testing.T) {
	resetDirCache()
	dir := t.TempDir()
	for i := 0; i < 10; i++ {
		_ = os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644)
	}
	old := time.Now().Add(-time.Second)
	_ = os.Chtimes(dir, old, old)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				entries, err := readDirCached(dir)
				if err != nil || len(entries) != 1 {
					t.Errorf("并发读目录异常：%v %+v", err, entries)
					return
				}
			}
		}()
	}
	wg.Wait()
}
