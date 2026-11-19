package tools

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// clipMarker is the separator clipTo inserts between the retained head and tail.
// It is pure ASCII, so it cannot by itself break UTF-8 validity.
const clipMarker = "\n... [truncated] ...\n"

// splitClipped splits a clipped result into its retained head/tail. It reports
// whether the marker (hence truncation) was present.
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

// assertClipInvariants checks the core invariant from AGENTS.md: tool output is
// never truncated in the middle of a UTF-8 rune. It also checks the byte/rune
// budget for the retained content (the marker is fixed ASCII overhead).
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
		// The trailing 4-byte emoji makes len(text)-tail land mid-rune while
		// text[tail] is a rune start: this used to produce invalid UTF-8.
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

// TestClipToStaysValidAcrossLimits sweeps every limit around each text length so
// that head/tail offsets land on every possible byte position within runes.
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
		// Marker overhead is constant and does not scale with the input.
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
