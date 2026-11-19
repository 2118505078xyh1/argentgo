package tui

import (
	"reflect"
	"testing"
)

func TestRuneWidthMeasuresCJKAndControls(t *testing.T) {
	cases := []struct {
		r    rune
		want int
	}{
		{'a', 1},
		{'你', 2},
		{'日', 2},
		{'😀', 2},
		{'\u0301', 0},
		{'\u0007', 0},
	}
	for _, tc := range cases {
		if got := RuneWidth(tc.r); got != tc.want {
			t.Errorf("RuneWidth(%q) = %d, want %d", tc.r, got, tc.want)
		}
	}
	if got := TextWidth("你好 hello"); got != 10 {
		t.Errorf("TextWidth(你好 hello) = %d, want 10", got)
	}
}

func TestClipAndTruncateRespectCellBoundaries(t *testing.T) {
	if got := Clip("你好世界", 5); got != "你好" {
		t.Errorf("Clip = %q, want 你好 (a wide rune never straddles the edge)", got)
	}
	if got := TextWidth(Clip("你好世界", 5)); got != 4 {
		t.Errorf("clipped width = %d, want 4", got)
	}
	if got := Clip("abcdef", 3); got != "abc" {
		t.Errorf("Clip = %q, want abc", got)
	}
	if got := Truncate("abcdef", 4); got != "abc…" {
		t.Errorf("Truncate = %q, want abc…", got)
	}
	if got := Pad("你好", 5); TextWidth(got) != 5 {
		t.Errorf("Pad width = %d, want 5", TextWidth(got))
	}
	if got := Pad("abcdef", 3); got != "abc" {
		t.Errorf("Pad = %q, want abc", got)
	}
}

func TestWrapKeepsWordsAndRespectsWidth(t *testing.T) {
	text := "the quick brown fox jumps over the lazy dog"
	lines := Wrap(text, 12)
	if len(lines) < 2 {
		t.Fatalf("expected several lines, got %v", lines)
	}
	var words []string
	for _, line := range lines {
		if TextWidth(line) > 12 {
			t.Errorf("line %q is %d cells wide", line, TextWidth(line))
		}
		for _, word := range wordsOf(line) {
			words = append(words, word)
		}
	}
	if want := wordsOf(text); !reflect.DeepEqual(words, want) {
		t.Errorf("words = %v, want %v", words, want)
	}
}

func TestWrapHardBreaksCJK(t *testing.T) {
	text := "中文没有空格"
	lines := Wrap(text, 4)
	if len(lines) < 2 {
		t.Fatalf("expected a hard break, got %v", lines)
	}
	joined := ""
	for _, line := range lines {
		if TextWidth(line) > 4 {
			t.Errorf("line %q is %d cells wide", line, TextWidth(line))
		}
		joined += line
	}
	if joined != text {
		t.Errorf("joined = %q, want %q", joined, text)
	}
}

func TestWrapOfEmptyTextIsOneEmptyLine(t *testing.T) {
	if got := Wrap("", 10); len(got) != 1 || got[0] != "" {
		t.Fatalf("Wrap(\"\") = %v, want one empty line", got)
	}
}

func TestCompactCountAndClock(t *testing.T) {
	counts := map[int]string{0: "0", 942: "942", 8200: "8.2k", 1600000: "1.6M"}
	for input, want := range counts {
		if got := compactCount(input); got != want {
			t.Errorf("compactCount(%d) = %s, want %s", input, got, want)
		}
	}
	clocks := map[int]string{7: "0:07", 724: "12:04", 3723: "1:02:03"}
	for input, want := range clocks {
		if got := clock(input); got != want {
			t.Errorf("clock(%d) = %s, want %s", input, got, want)
		}
	}
}

func TestSummarizeArgsPicksThePrimaryValue(t *testing.T) {
	if got := summarizeArgs(`{"path":"a/b.go","content":"x"}`); got != "a/b.go" {
		t.Errorf("summarizeArgs = %q, want a/b.go", got)
	}
	if got := summarizeArgs(`{"command":"go test ./..."}`); got != "go test ./..." {
		t.Errorf("summarizeArgs = %q, want the command", got)
	}
	if got := summarizeArgs(""); got != "" {
		t.Errorf("summarizeArgs(\"\") = %q, want empty", got)
	}
	if got := summarizeArgs("not json"); got != "not json" {
		t.Errorf("summarizeArgs(raw) = %q", got)
	}
}

func wordsOf(text string) []string {
	var out []string
	current := ""
	for _, r := range text {
		if r == ' ' {
			if current != "" {
				out = append(out, current)
				current = ""
			}
			continue
		}
		current += string(r)
	}
	if current != "" {
		out = append(out, current)
	}
	return out
}
