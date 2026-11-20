package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本文件记录会话热路径的性能基线：JSONL
// 追加、完整转录解析以及 List/旁车索引。所有输入都是
// 写入 b.TempDir() 的固定内存夹具；不涉及网络、模型或真实的
// 时钟依赖。不断言任何时钟阈值——
// 这些数字的存在只是为了让后续运行可以比较。

var benchMetaSink Meta

// benchWriteTranscript 写入一个包含 n 条格式良好记录的 JSONL 文件。
// 时间戳是固定的，因此重复运行产生逐字节相同的文件。
func benchWriteTranscript(b *testing.B, path string, n int, payload string) {
	b.Helper()
	stamp := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	data, err := json.Marshal(map[string]string{"role": "user", "content": payload})
	if err != nil {
		b.Fatal(err)
	}
	var sb strings.Builder
	sb.Grow(n * (len(payload) + 128))
	for i := 1; i <= n; i++ {
		parent := ""
		if i > 1 {
			parent = fmt.Sprintf("n%06d", i-1)
		}
		record := Record{
			ID:     fmt.Sprintf("n%06d", i),
			Parent: parent,
			TS:     stamp,
			Kind:   KindMessage,
			Data:   data,
		}
		line, err := json.Marshal(record)
		if err != nil {
			b.Fatal(err)
		}
		sb.Write(line)
		sb.WriteByte('\n')
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		b.Fatal(err)
	}
}

// benchTranscriptPath 返回新写入的夹具转录的路径。
func benchTranscriptPath(b *testing.B, n int, payload string) string {
	b.Helper()
	path := filepath.Join(b.TempDir(), "bench.jsonl")
	benchWriteTranscript(b, path, n, payload)
	return path
}

// BenchmarkAppendRecord 测量稳态 AppendRecord 吞吐量：header 之后
// 索引已预热，因此每次迭代覆盖载荷编码、
// id 生成、一次 JSONL 写入以及写入后的 stat/sync。
func BenchmarkAppendRecord(b *testing.B) {
	cases := []struct {
		name    string
		payload string
	}{
		{"small-16B", strings.Repeat("x", 16)},
		{"medium-1KB", strings.Repeat("abcdefghij", 100)},
		{"large-16KB", strings.Repeat("abcdefghij", 1600)},
	}
	for _, bc := range cases {
		b.Run(bc.name, func(b *testing.B) {
			store, err := Open(b.TempDir())
			if err != nil {
				b.Fatal(err)
			}
			s, err := store.NewSession("append-bench")
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := s.AppendRecord(KindMessage, map[string]string{
					"role": "user", "content": bc.payload,
				}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkReadRecords 测量每次历史读取所用的完整 JSONL 解析，
// 覆盖性能简报中提到的三种历史规模。
func BenchmarkReadRecords(b *testing.B) {
	for _, n := range []int{100, 1000, 5000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			path := benchTranscriptPath(b, n, strings.Repeat("x", 96))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				records, err := readRecords(path)
				// readRecords 会为 v0 转录在前面加一条合成 schema 头，
				// 因此 n 条记录的夹具解析为 n+1 条。
				if err != nil || len(records) != n+1 {
					b.Fatalf("readRecords: %d records, err %v; want %d", len(records), err, n+1)
				}
			}
		})
	}
}

// BenchmarkSessionRecords 测量公开的 Records() 包装器，它读取
// 的是 Session 自身的路径，而不是原始文件路径。
func BenchmarkSessionRecords(b *testing.B) {
	for _, n := range []int{100, 1000, 5000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			path := benchTranscriptPath(b, n, strings.Repeat("x", 96))
			s := &Session{dir: filepath.Dir(path), id: strings.TrimSuffix(filepath.Base(path), fileExt)}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				records, err := s.Records()
				// Records() 暴露与 readRecords 相同的合成 schema 头，
				// 因此 n 条记录的夹具产生 n+1 条。
				if err != nil || len(records) != n+1 {
					b.Fatalf("Records: %d records, err %v; want %d", len(records), err, n+1)
				}
			}
		})
	}
}

// BenchmarkList 测量旁车文件预热后的 store 列表：每次调用
// 仍会校验每份转录，但从 .idx 缓存读取元数据，
// 而不是解析整个文件。
func BenchmarkList(b *testing.B) {
	for _, n := range []int{10, 100, 500} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			dir := b.TempDir()
			for i := 0; i < n; i++ {
				benchWriteTranscript(b, filepath.Join(dir, fmt.Sprintf("bench-%04d.jsonl", i)), 4, "list entry")
			}
			// 一次预热 List 构建旁车文件；计时循环是热路径。
			if _, err := List(dir); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				metas, err := List(dir)
				if err != nil || len(metas) != n {
					b.Fatalf("List: %d metas, err %v; want %d", len(metas), err, n)
				}
			}
		})
	}
}

// BenchmarkMetaFromRecords 隔离冷 List 在没有可用旁车文件时，
// 对每份转录运行的索引构建器。
func BenchmarkMetaFromRecords(b *testing.B) {
	for _, n := range []int{100, 1000, 5000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			path := benchTranscriptPath(b, n, strings.Repeat("x", 96))
			records, err := readRecords(path)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				benchMetaSink = metaFromRecords("bench", records)
			}
		})
	}
}
