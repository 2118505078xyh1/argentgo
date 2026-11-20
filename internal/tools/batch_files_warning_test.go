package tools

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTooManyFilesWarningBelowThresholdsIsEmpty(t *testing.T) {
	cases := []struct {
		name                   string
		fileCount, totalTokens int
		maxFiles, maxTokens    int
	}{
		{"well below both", 3, 1000, 10, 5000},
		{"exactly at both", 10, 5000, 10, 5000},
		{"files at limit, tokens below", 10, 100, 10, 5000},
		{"tokens at limit, files below", 2, 5000, 10, 5000},
		{"all zeros", 0, 0, 0, 0},
		{"negative current values", -5, -100, 10, 5000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tooManyFilesWarning(tc.fileCount, tc.totalTokens, tc.maxFiles, tc.maxTokens); got != "" {
				t.Fatalf("expected empty warning, got %q", got)
			}
		})
	}
}

func TestTooManyFilesWarningOverThresholdsReturnsChineseWarning(t *testing.T) {
	cases := []struct {
		name                   string
		fileCount, totalTokens int
		maxFiles, maxTokens    int
	}{
		{"files only", 11, 100, 10, 5000},
		{"tokens only", 2, 5001, 10, 5000},
		{"both", 11, 5001, 10, 5000},
		{"far over both", 1000, 999999, 10, 5000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tooManyFilesWarning(tc.fileCount, tc.totalTokens, tc.maxFiles, tc.maxTokens)
			if got == "" {
				t.Fatal("expected a warning, got empty string")
			}
			if !strings.Contains(got, "只加入需要改动的文件") {
				t.Fatalf("warning %q missing required guidance", got)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("warning is not valid UTF-8: %q", got)
			}
			if strings.ContainsRune(got, utf8.RuneError) {
				t.Fatalf("warning contains a replacement rune: %q", got)
			}
			if n := utf8.RuneCountInString(got); n > tooManyFilesWarningMaxRunes {
				t.Fatalf("warning has %d runes, exceeds bound %d: %q", n, tooManyFilesWarningMaxRunes, got)
			}
		})
	}
}

func TestTooManyFilesWarningDisabledLimits(t *testing.T) {
	cases := []struct {
		name                   string
		fileCount, totalTokens int
		maxFiles, maxTokens    int
		wantWarn               bool
	}{
		{"both disabled", 1000000, 99999999, 0, 0, false},
		{"files disabled by zero, tokens over", 100, 5001, 0, 5000, true},
		{"tokens disabled by negative, files over", 11, 999999, 10, -1, true},
		{"negative files limit ignored", 11, 100, -5, 5000, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tooManyFilesWarning(tc.fileCount, tc.totalTokens, tc.maxFiles, tc.maxTokens)
			if tc.wantWarn && got == "" {
				t.Fatal("expected a warning, got empty string")
			}
			if !tc.wantWarn && got != "" {
				t.Fatalf("expected empty warning, got %q", got)
			}
		})
	}
}

func TestTooManyFilesWarningIsPure(t *testing.T) {
	first := tooManyFilesWarning(20, 9000, 10, 5000)
	for i := 0; i < 5; i++ {
		if got := tooManyFilesWarning(20, 9000, 10, 5000); got != first {
			t.Fatalf("call %d = %q, want stable %q (caller owns one-time dedupe)", i, got, first)
		}
	}
}

func TestTooManyFilesWarningBoundedWithHugeNumbers(t *testing.T) {
	const big = int(^uint(0) >> 1) // 平台最大 int
	got := tooManyFilesWarning(big, big, 10, 5000)
	if got == "" {
		t.Fatal("expected a warning for huge inputs")
	}
	if !utf8.ValidString(got) {
		t.Fatalf("not valid UTF-8: %q", got)
	}
	if n := utf8.RuneCountInString(got); n > tooManyFilesWarningMaxRunes {
		t.Fatalf("warning not bounded: %d runes", n)
	}
}

func TestClipWarningRunesCJKSafeAndBounded(t *testing.T) {
	input := strings.Repeat("文", 1000)
	got := clipWarningRunes(input, 20)
	if n := utf8.RuneCountInString(got); n != 20 {
		t.Fatalf("clipped runes = %d, want 20: %q", n, got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("clipped text is not valid UTF-8: %q", got)
	}
	if strings.ContainsRune(got, utf8.RuneError) {
		t.Fatalf("clipped text contains a replacement rune: %q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("clipped text missing ellipsis: %q", got)
	}
}

func TestClipWarningRunesShortAndEdgeCases(t *testing.T) {
	if got := clipWarningRunes("短文本", 10); got != "短文本" {
		t.Fatalf("short text should pass through unchanged, got %q", got)
	}
	if got := clipWarningRunes("任意文本", 0); got != "" {
		t.Fatalf("non-positive max should give empty string, got %q", got)
	}
	if got := clipWarningRunes("任意文本", -1); got != "" {
		t.Fatalf("negative max should give empty string, got %q", got)
	}
}
