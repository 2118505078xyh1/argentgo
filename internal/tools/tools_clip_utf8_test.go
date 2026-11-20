package tools

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// clipMarker 是 clipTo 在保留的头尾之间插入的分隔符。
// 它是纯 ASCII，因此本身不会破坏 UTF-8 有效性。
const clipMarker = "\n... [truncated] ...\n"

// splitClipped 把截断结果拆成保留的头/尾。它报告
// 标记（因而截断）是否存在。
func splitClipped(t *testing.T, got string) (head, tail string, truncated bool) {
	t.Helper()
	if !strings.Contains(got, clipMarker) {
		return got, "", false
	}
	parts := strings.Split(got, clipMarker)
	if len(parts) != 2 {
		t.Fatalf("clipped result has %d markers: %q", len(parts)-1, got)
	}
	return parts[0], parts[1], true
}

// assertClipInvariants 检查 AGENTS.md 中的核心不变量：工具输出
// 绝不会在 UTF-8 rune 中间被截断。它还检查保留内容的
// 字节/rune 预算（标记是固定的 ASCII 开销）。
func assertClipInvariants(t *testing.T, text string, limit int, got string) {
	t.Helper()
	budget := limit
	if budget <= 0 {
		budget = maxToolOutput // clipTo treats non-positive limits as the default.
	}
	if !utf8.ValidString(got) {
		t.Fatalf("clipTo(..., %d) produced invalid UTF-8: %q", limit, got)
	}
	head, tail, truncated := splitClipped(t, got)
	if len(text) <= budget {
		if truncated || got != text {
			t.Fatalf("clipTo(..., %d) = %q, want unchanged %q", limit, got, text)
		}
		return
	}
	if !truncated {
		t.Fatalf("clipTo(..., %d) = %q, expected truncation marker", limit, got)
	}
	if runes := utf8.RuneCountInString(head) + utf8.RuneCountInString(tail); runes > budget {
		t.Fatalf("clipTo(..., %d) kept %d runes, budget is %d", limit, runes, budget)
	}
	if bytes := len(head) + len(tail); bytes > budget {
		t.Fatalf("clipTo(..., %d) kept %d bytes, budget is %d", limit, bytes, budget)
	}
}

func TestClipToNeverCutsUTF8Rune(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		limit int
	}{
		{"empty", "", 8},
		{"all ascii", "abcdefghijklmnopqrstuvwxyz", 10},
		{"ascii within limit", "abc", 10},
		{"all chinese", strings.Repeat("中文测试", 8), 12},
		{"all emoji", strings.Repeat("😀🚀", 10), 15},
		{"combining marks", strings.Repeat("e\u0301a\u0301", 8), 10},
		{"mixed scripts", strings.Repeat("ab中😀c\u0301", 6), 13},
		{"limit inside three byte rune", "中文测试中文测试", 7},
		{"limit inside four byte emoji", "😀😀😀😀😀😀", 5},
		// 尾随的四字节 emoji 使 len(text)-tail 落在 rune 中间，
		// 而 text[tail] 是一个 rune 起点：这过去会产生无效 UTF-8。
		{"tail boundary mid emoji", "中中😀中中😀", 9},
		{"limit one", strings.Repeat("中", 12), 1},
		{"limit two", strings.Repeat("中", 12), 2},
		{"zero limit uses default", "短文本😀", 0},
		{"negative limit uses default", strings.Repeat("中", 5), -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertClipInvariants(t, tc.text, tc.limit, clipTo(tc.text, tc.limit))
		})
	}
}

// TestClipToStaysValidAcrossLimits 遍历每段文本长度附近的所有限制，
// 使头/尾偏移落在 rune 内每个可能的字节位置上。
func TestClipToStaysValidAcrossLimits(t *testing.T) {
	texts := []string{
		"abcdefghij",
		strings.Repeat("中文", 9),
		strings.Repeat("😀", 9),
		strings.Repeat("ab中😀e\u0301", 4),
		"中中😀中中😀",
	}
	for _, text := range texts {
		for limit := 0; limit <= len(text)+2; limit++ {
			assertClipInvariants(t, text, limit, clipTo(text, limit))
		}
	}
}

func TestClipToRespectsByteBudget(t *testing.T) {
	text := strings.Repeat("中😀ab\u0301", 20)
	for _, limit := range []int{3, 7, 12, 25, 60, 200} {
		got := clipTo(text, limit)
		head, tail, truncated := splitClipped(t, got)
		if !truncated {
			t.Fatalf("clipTo(..., %d) did not truncate a %d-byte text", limit, len(text))
		}
		if kept := len(head) + len(tail); kept > limit {
			t.Fatalf("clipTo(..., %d) kept %d bytes, over budget", limit, kept)
		}
		// 标记开销恒定，不随输入规模变化。
		if removed := len(got) - (len(head) + len(tail)); removed != len(clipMarker) {
			t.Fatalf("clipTo(..., %d) marker overhead = %d bytes, want %d", limit, removed, len(clipMarker))
		}
	}
}

func TestTrimPartialRuneBacksOffToRuneStart(t *testing.T) {
	const chinese = "中文abc" // 中=0..2, 文=3..5, a=6, b=7, c=8 (len 9)
	const emoji = "a😀b"     // a=0, 😀=1..4, b=5 (len 6)
	cases := []struct {
		name   string
		text   string
		offset int
		want   int
	}{
		{"zero", chinese, 0, 0},
		{"negative", chinese, -1, 0},
		{"at rune start", chinese, 3, 3},
		{"inside first rune at one", chinese, 1, 0},
		{"inside first rune at two", chinese, 2, 0},
		{"inside second rune", chinese, 4, 3},
		{"ascii byte is a boundary", chinese, 6, 6},
		{"inside emoji rune", emoji, 2, 1},
		{"at emoji rune start", emoji, 1, 1},
		{"at end of text", chinese, len(chinese), len(chinese)},
		{"past end of text", chinese, 20, 20},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := trimPartialRune(tc.text, tc.offset); got != tc.want {
				t.Fatalf("trimPartialRune(%q, %d) = %d, want %d", tc.text, tc.offset, got, tc.want)
			}
		})
	}
}
