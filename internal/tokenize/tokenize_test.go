package tokenize

import (
	"strings"
	"testing"
)

func TestEstimateASCIIRoundsUp(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"a", 1},
		{"abcd", 1},
		{"abcde", 2},
		{strings.Repeat("a", 400), 100},
	}
	for _, tc := range cases {
		if got := Estimate(tc.in); got != tc.want {
			t.Fatalf("Estimate(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestEstimateWideRunesCostOneEach(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"han", strings.Repeat("中", 100), 100},
		{"kana and hangul", "アイウ한글", 5},
		{"emoji", "\U0001F600\U0001F600", 2},
		{"mixed", "中文测试", 4},
		{"han plus ascii", "abcd中文", 2 + 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Estimate(tc.in); got != tc.want {
				t.Fatalf("Estimate(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestIsWideCoversTheSharedRanges(t *testing.T) {
	cases := []struct {
		r    rune
		want bool
	}{
		{'a', false},
		{0x10FF, false},
		{0x1100, true},
		{'中', true},
		{'カ', true},
		{'한', true},
		{0xFF21, true},
		{0x1F600, true},
		{0x20000, true},
		{0x40000, false},
	}
	for _, tc := range cases {
		if got := IsWide(tc.r); got != tc.want {
			t.Fatalf("IsWide(%#U) = %v, want %v", tc.r, got, tc.want)
		}
	}
}
