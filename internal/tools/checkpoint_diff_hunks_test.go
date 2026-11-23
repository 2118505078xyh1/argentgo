package tools

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestCheckpointDiffHunks 锁定 #44：FileChange 携带逐行 hunk，
// 头部带 +N/-M，CJK 行按 rune 安全保留；binary/超大/超时有界降级。
func TestCheckpointDiffHunks(t *testing.T) {
	t.Run("line hunks with CJK", func(t *testing.T) {
		before := []byte("package main\n\nfunc old() {}\n// 中文注释\n")
		after := []byte("package main\n\nfunc new() {}\n// 中文注释\n")
		change := FileChangeFromBytes("main.go", before, after)
		if len(change.Hunks) == 0 {
			t.Fatalf("no hunks generated: %+v", change)
		}
		hunk := change.Hunks[0]
		if !strings.Contains(hunk.Header, "+1/-1") {
			t.Fatalf("hunk header missing counts: %q", hunk.Header)
		}
		if !strings.Contains(hunk.Header, "@@") {
			t.Fatalf("hunk header missing range: %q", hunk.Header)
		}
		joined := strings.Join(hunk.Lines, "\n")
		if !strings.Contains(joined, "-func old() {}") || !strings.Contains(joined, "+func new() {}") {
			t.Fatalf("hunk lines missing edits: %q", joined)
		}
		if !strings.Contains(joined, "中文注释") {
			t.Fatalf("CJK context line was corrupted: %q", joined)
		}
		if change.Added != 1 || change.Removed != 1 {
			t.Fatalf("counts = (%d, %d), want (1, 1)", change.Added, change.Removed)
		}
	})

	t.Run("hunks survive JSON rendering", func(t *testing.T) {
		change := FileChangeFromBytes("f.txt", []byte("a\nb\n"), []byte("a\nc\n"))
		out := renderCheckpointDiff([]FileChange{change})
		var decoded checkpointDiffOutput
		if err := json.Unmarshal([]byte(out), &decoded); err != nil {
			t.Fatalf("rendered diff is not JSON: %v (%s)", err, out)
		}
		if len(decoded.Files) != 1 || len(decoded.Files[0].Hunks) == 0 {
			t.Fatalf("hunks did not survive rendering: %s", out)
		}
	})

	t.Run("timeout degrades to counts only", func(t *testing.T) {
		var before, after strings.Builder
		for i := 0; i < 300; i++ {
			before.WriteString("same line\n")
			after.WriteString("same line\n")
		}
		after.WriteString("extra line\n")
		res := computeLineDiffDeadline([]byte(before.String()), []byte(after.String()), time.Now().Add(-time.Second))
		if !res.degraded {
			t.Fatal("past deadline should force degradation")
		}
		if len(res.hunks) != 0 {
			t.Fatalf("degraded result should carry no hunks, got %d", len(res.hunks))
		}
		// 与既有超大输入回退一致：超时降级为整文件删除 + 整文件新增的粗计数。
		if res.added != 301 || res.removed != 300 {
			t.Fatalf("degraded counts = (%d, %d), want (301, 300)", res.added, res.removed)
		}
	})

	t.Run("binary only gets a flag", func(t *testing.T) {
		change := FileChangeFromBytes("bin.dat", []byte("a\x00b"), []byte("a\x00c"))
		if !change.Binary || len(change.Hunks) != 0 {
			t.Fatalf("binary change = %+v, want flag and no hunks", change)
		}
	})

	t.Run("oversized only gets a flag", func(t *testing.T) {
		big := strings.Repeat("x\n", maxDiffBytesForHunks/2+1)
		change := FileChangeFromBytes("big.txt", nil, []byte(big))
		if !change.Oversized || len(change.Hunks) != 0 {
			t.Fatalf("oversized change = %+v, want flag and no hunks", change)
		}
	})

	t.Run("hunks bounded by line budget", func(t *testing.T) {
		var b strings.Builder
		for i := 0; i < maxHunkLinesForDiff+50; i++ {
			b.WriteString("old line\n")
		}
		after := strings.ReplaceAll(b.String(), "old", "new")
		change := FileChangeFromBytes("many.txt", []byte(b.String()), []byte(after))
		total := 0
		for _, h := range change.Hunks {
			total += len(h.Lines)
		}
		if total > maxHunkLinesForDiff {
			t.Fatalf("hunk lines = %d, want <= %d", total, maxHunkLinesForDiff)
		}
		if !change.Degraded {
			t.Fatal("truncated hunks should be marked degraded")
		}
	})
}
