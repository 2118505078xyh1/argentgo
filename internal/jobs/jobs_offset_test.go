package jobs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestSliceOutputByOffsetReadsFromOffset 验证基本的按绝对字节偏移读取与截断标记。
func TestSliceOutputByOffsetReadsFromOffset(t *testing.T) {
	data := []byte("abcdef")
	chunk, next, truncated := sliceOutputByOffset(data, 2, 3)
	if string(chunk) != "cde" {
		t.Fatalf("chunk = %q, want %q", chunk, "cde")
	}
	if next != 5 {
		t.Fatalf("next = %d, want 5", next)
	}
	if !truncated {
		t.Fatal("truncated = false, want true when bytes remain")
	}

	// 从 next 续读，覆盖末尾，不再截断。
	chunk, next, truncated = sliceOutputByOffset(data, next, 3)
	if string(chunk) != "f" {
		t.Fatalf("chunk = %q, want %q", chunk, "f")
	}
	if next != int64(len(data)) {
		t.Fatalf("next = %d, want %d", next, len(data))
	}
	if truncated {
		t.Fatal("truncated = true, want false at end of data")
	}
}

// TestSliceOutputByOffsetRespectsRuneBoundary 验证 limit 切断多字节 rune 时按 rune 对齐，且保证前进。
func TestSliceOutputByOffsetRespectsRuneBoundary(t *testing.T) {
	data := []byte("AB汉C") // A=0, B=1, 汉=E6 B1 89@2..4, C=5

	// limit 落在"汉"的中间：整体纳入而非切出半个字，并继续从 rune 边界读取。
	chunk, next, truncated := sliceOutputByOffset(data, 2, 2)
	if string(chunk) != "汉" {
		t.Fatalf("chunk = %q, want %q", chunk, "汉")
	}
	if next != 5 {
		t.Fatalf("next = %d, want 5", next)
	}
	if !truncated {
		t.Fatal("truncated = false, want true when limit cut the rune")
	}

	// offset 落在 rune 中间的续字节上：跳到下一个 rune 起始（有损耗）。
	chunk, next, truncated = sliceOutputByOffset(data, 3, 10)
	if string(chunk) != "C" {
		t.Fatalf("chunk = %q, want %q", chunk, "C")
	}
	if next != int64(len(data)) {
		t.Fatalf("next = %d, want %d", next, len(data))
	}
	if !truncated {
		t.Fatal("truncated = false, want true for a lossy leading skip")
	}
}

// TestSliceOutputByOffsetCJK 验证 CJK 多字节字符不会被切断。
func TestSliceOutputByOffsetCJK(t *testing.T) {
	data := []byte("你好世界") // 每字 3 字节：你@0..2 好@3..5 世@6..8 界@9..11

	chunk, next, truncated := sliceOutputByOffset(data, 3, 3)
	if string(chunk) != "好" {
		t.Fatalf("chunk = %q, want %q", chunk, "好")
	}
	if next != 6 {
		t.Fatalf("next = %d, want 6", next)
	}
	if !truncated {
		t.Fatal("truncated = false, want true while bytes remain")
	}

	// offset 落在"好"的中间字节：跳过残片后从"世"开始，并标记为有损。
	chunk, next, truncated = sliceOutputByOffset(data, 4, 100)
	if string(chunk) != "世界" {
		t.Fatalf("chunk = %q, want %q", chunk, "世界")
	}
	if next != int64(len(data)) {
		t.Fatalf("next = %d, want %d", next, len(data))
	}
	if !truncated {
		t.Fatal("truncated = false, want true for a lossy leading skip")
	}
}

// TestSliceOutputByOffsetBeyondLength 验证 offset 超长或恰好等于长度时返回空块。
func TestSliceOutputByOffsetBeyondLength(t *testing.T) {
	data := []byte("abc")
	for _, offset := range []int64{3, 4, 100, 1 << 40} {
		chunk, next, truncated := sliceOutputByOffset(data, offset, 10)
		if len(chunk) != 0 {
			t.Fatalf("offset %d: chunk = %q, want empty", offset, chunk)
		}
		if next != int64(len(data)) {
			t.Fatalf("offset %d: next = %d, want %d", offset, next, len(data))
		}
		if truncated {
			t.Fatalf("offset %d: truncated = true, want false", offset)
		}
	}
}

// TestSliceOutputByOffsetNegativeClampsToZero 验证负 offset 被钳到零。
func TestSliceOutputByOffsetNegativeClampsToZero(t *testing.T) {
	data := []byte("abc")
	for _, offset := range []int64{-1, -5, -1 << 40} {
		chunk, next, truncated := sliceOutputByOffset(data, offset, 2)
		if string(chunk) != "ab" {
			t.Fatalf("offset %d: chunk = %q, want %q", offset, chunk, "ab")
		}
		if next != 2 {
			t.Fatalf("offset %d: next = %d, want 2", offset, next)
		}
		if !truncated {
			t.Fatalf("offset %d: truncated = false, want true", offset)
		}
	}
}

// TestSliceOutputByOffsetZeroLimit 验证 limit<=0 表示不限制字节数，读取全部剩余数据。
func TestSliceOutputByOffsetZeroLimit(t *testing.T) {
	data := []byte("你好世界")
	for _, limit := range []int{0, -1} {
		chunk, next, truncated := sliceOutputByOffset(data, 0, limit)
		if string(chunk) != string(data) {
			t.Fatalf("limit %d: chunk = %q, want %q", limit, chunk, data)
		}
		if next != int64(len(data)) {
			t.Fatalf("limit %d: next = %d, want %d", limit, next, len(data))
		}
		if truncated {
			t.Fatalf("limit %d: truncated = true, want false", limit)
		}
	}
}

// TestSliceOutputByOffsetIdempotent 验证按 nextOffset 连续读取可无重叠、无丢失地还原原始输出。
func TestSliceOutputByOffsetIdempotent(t *testing.T) {
	text := "hi 世界 café 😀 abc 日本語 end"
	data := []byte(text)

	var got strings.Builder
	offset := int64(0)
	for i := 0; i < 1000; i++ {
		chunk, next, truncated := sliceOutputByOffset(data, offset, 3)
		if len(chunk) > 0 && !utf8.Valid(chunk) {
			t.Fatalf("chunk at offset %d is not valid UTF-8: %q", offset, chunk)
		}
		if truncated && next <= offset {
			t.Fatalf("no progress at offset %d with limit=3", offset)
		}
		got.Write(chunk)
		offset = next
		if !truncated {
			break
		}
	}
	if got.String() != text {
		t.Fatalf("reassembled = %q, want %q", got.String(), text)
	}
	if offset != int64(len(data)) {
		t.Fatalf("final offset = %d, want %d", offset, len(data))
	}
}

// TestTailFromOffsetIncremental 验证接线点：按绝对文件偏移增量读取真实日志并可幂等续读。
func TestTailFromOffsetIncremental(t *testing.T) {
	registry := newRegistry(t)
	text := "line1\n你好\nline3\n😀\n"
	logPath := filepath.Join(registry.Dir(), "job-offset.log")
	if err := os.WriteFile(logPath, []byte(text), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}
	job := Job{ID: "job-offset", LogPath: logPath}
	registry.mu.Lock()
	registry.jobs[job.ID] = &job
	registry.mu.Unlock()

	var got strings.Builder
	offset := int64(0)
	for i := 0; i < 1000; i++ {
		chunk, next, truncated, err := registry.TailFromOffset(job.ID, offset, 4)
		if err != nil {
			t.Fatalf("tail from offset: %v", err)
		}
		if !utf8.ValidString(chunk) {
			t.Fatalf("chunk at offset %d is not valid UTF-8: %q", offset, chunk)
		}
		if truncated && next <= offset {
			t.Fatalf("no progress at offset %d", offset)
		}
		got.WriteString(chunk)
		offset = next
		if !truncated {
			break
		}
	}
	if got.String() != text {
		t.Fatalf("reassembled = %q, want %q", got.String(), text)
	}

	// 重连：用保存的绝对偏移读取追加内容，不重复已消费部分。
	if err := os.WriteFile(logPath, []byte(text+"more\n"), 0o644); err != nil {
		t.Fatalf("append log: %v", err)
	}
	chunk, _, _, err := registry.TailFromOffset(job.ID, offset, 0)
	if err != nil {
		t.Fatalf("tail after append: %v", err)
	}
	if chunk != "more\n" {
		t.Fatalf("chunk after append = %q, want %q", chunk, "more\n")
	}
}
