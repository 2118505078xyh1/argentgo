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

// This file records performance baselines for the session hot paths: JSONL
// append, full transcript parsing, and the List/sidecar index. All inputs are
// fixed in-memory fixtures written to b.TempDir(); no network, model or real
// wall-clock dependency is involved. No wall-clock thresholds are asserted —
// the numbers exist only so later runs can be compared.

var benchMetaSink Meta

// benchWriteTranscript writes a JSONL file with n well-formed records. The
// timestamp is fixed so repeated runs produce byte-identical files.
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

// benchTranscriptPath returns the path of a freshly written fixture transcript.
func benchTranscriptPath(b *testing.B, n int, payload string) string {
	b.Helper()
	path := filepath.Join(b.TempDir(), "bench.jsonl")
	benchWriteTranscript(b, path, n, payload)
	return path
}

// BenchmarkAppendRecord measures steady-state AppendRecord throughput: the
// index is warm after the header, so each iteration exercises payload encoding,
// id minting, one JSONL write and the post-write stat/sync.
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

// BenchmarkReadRecords measures the full JSONL parse used by every history
// read, across the three history sizes called out in the performance brief.
func BenchmarkReadRecords(b *testing.B) {
	for _, n := range []int{100, 1000, 5000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			path := benchTranscriptPath(b, n, strings.Repeat("x", 96))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				records, err := readRecords(path)
				// readRecords prepends one synthetic schema header for v0
				// transcripts, so a fixture of n records parses to n+1.
				if err != nil || len(records) != n+1 {
					b.Fatalf("readRecords: %d records, err %v; want %d", len(records), err, n+1)
				}
			}
		})
	}
}

// BenchmarkSessionRecords measures the public Records() wrapper, which reads
// through the Session's own path rather than a raw file path.
func BenchmarkSessionRecords(b *testing.B) {
	for _, n := range []int{100, 1000, 5000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			path := benchTranscriptPath(b, n, strings.Repeat("x", 96))
			s := &Session{dir: filepath.Dir(path), id: strings.TrimSuffix(filepath.Base(path), fileExt)}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				records, err := s.Records()
				// Records() exposes the same synthetic schema header readRecords
				// prepends, so a fixture of n records yields n+1.
				if err != nil || len(records) != n+1 {
					b.Fatalf("Records: %d records, err %v; want %d", len(records), err, n+1)
				}
			}
		})
	}
}

// BenchmarkList measures the store listing after sidecars are warm: each call
// still validates every transcript but reads metadata from the .idx cache
// instead of parsing the whole file.
func BenchmarkList(b *testing.B) {
	for _, n := range []int{10, 100, 500} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			dir := b.TempDir()
			for i := 0; i < n; i++ {
				benchWriteTranscript(b, filepath.Join(dir, fmt.Sprintf("bench-%04d.jsonl", i)), 4, "list entry")
			}
			// One warm-up List builds the sidecars; the timed loop is the hot path.
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

// BenchmarkMetaFromRecords isolates the index builder that a cold List runs on
// every transcript without a usable sidecar.
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
