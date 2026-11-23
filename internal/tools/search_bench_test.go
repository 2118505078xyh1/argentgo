package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var benchSearchSink int

// BenchmarkGrepExStreaming 测量超过 4 MiB 阈值后的流式 grep：
// 内存占用应与文件大小无关（用 ReportAllocs 观察分配）。
func BenchmarkGrepExStreaming(b *testing.B) {
	dir := b.TempDir()
	line := strings.Repeat("a", 1000) + "\n"
	content := strings.Repeat(line, 5*1024) + "NEEDLE here\n" + strings.Repeat("tail\n", 100)
	path := filepath.Join(dir, "big.txt")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		b.Fatal(err)
	}
	resetDirCache()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		matches, _, _, err := GrepExWithOutcome(GrepExOptions{
			Pattern: "NEEDLE", Path: dir, Literal: true, CaseSensitive: true,
		})
		if err != nil || len(matches) != 1 {
			b.Fatalf("streaming grep broken: %d matches, %v", len(matches), err)
		}
		benchSearchSink = len(matches)
	}
}

// BenchmarkWalkFilesParallel 测量大量文件下的并行遍历 + 目录缓存命中。
func BenchmarkWalkFilesParallel(b *testing.B) {
	root := b.TempDir()
	for i := 0; i < 1000; i++ {
		sub := filepath.Join(root, fmt.Sprintf("pkg%02d", i%16))
		if err := os.MkdirAll(sub, 0o755); err != nil {
			b.Fatal(err)
		}
		name := filepath.Join(sub, fmt.Sprintf("f%04d.go", i))
		if err := os.WriteFile(name, []byte("package p\n"), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	old := time.Now().Add(-time.Second)
	_ = os.Chtimes(root, old, old)
	resetDirCache()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var n atomic.Int64
		walkFiles(context.Background(), root, func(string) bool {
			n.Add(1)
			return true
		})
		if got := n.Load(); got != 1000 {
			b.Fatalf("walk visited %d files, want 1000", got)
		}
		benchSearchSink = int(n.Load())
	}
}

// BenchmarkDirCacheHit 测量进程级目录列表缓存的命中路径（零 readdir）。
func BenchmarkDirCacheHit(b *testing.B) {
	resetDirCache()
	dir := b.TempDir()
	for i := 0; i < 100; i++ {
		_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%03d.go", i)), []byte("x"), 0o644)
	}
	old := time.Now().Add(-time.Second)
	_ = os.Chtimes(dir, old, old)
	if _, err := readDirCached(dir); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		entries, err := readDirCached(dir)
		if err != nil || len(entries) != 100 {
			b.Fatalf("cache hit broken: %d entries, %v", len(entries), err)
		}
		benchSearchSink = len(entries)
	}
}
