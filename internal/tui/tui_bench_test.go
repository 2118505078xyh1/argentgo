package tui

import (
	"fmt"
	"strings"
	"testing"
)

// This file records performance baselines for the TUI's per-frame hot paths:
// markdown rendering, grapheme-aware width/clip/wrap and line diffing. Fixtures
// are fixed strings; no terminal, network or real wall-clock dependency is
// involved. No wall-clock thresholds are asserted.

var (
	benchTuiInt    int
	benchTuiString string
	benchTuiLines  int
	benchTuiDiffs  []DiffLine
)

func benchTuiPalette(b *testing.B) Palette {
	b.Helper()
	p, err := paletteFor(ThemeDark)
	if err != nil {
		b.Fatal(err)
	}
	return p
}

// benchMarkdownText builds a markdown block of roughly n lines mixing headings,
// bullets, inline code, a fenced block and a table.
func benchMarkdownText(n int) string {
	var sb strings.Builder
	sb.WriteString("# Heading with **bold** and `code`\n")
	for i := 0; i < n; i++ {
		switch i % 5 {
		case 0:
			fmt.Fprintf(&sb, "- item %d with **strong** and `inline` text\n", i)
		case 1:
			fmt.Fprintf(&sb, "paragraph %d 中文 text with 😀 emoji and a very long tail that should wrap across lines\n", i)
		case 2:
			sb.WriteString("```go\nfunc Example() { return 中文 + \"😀\" }\n```\n")
		case 3:
			sb.WriteString("| left | right |\n|---|---|\n| a | b |\n")
		default:
			fmt.Fprintf(&sb, "plain line %d\n", i)
		}
	}
	return sb.String()
}

// BenchmarkMarkdownLines measures the renderer used for every assistant block.
func BenchmarkMarkdownLines(b *testing.B) {
	p := benchTuiPalette(b)
	for _, n := range []int{10, 100, 500} {
		b.Run(fmt.Sprintf("lines=%d", n), func(b *testing.B) {
			text := benchMarkdownText(n)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				benchTuiLines = len(markdownLines(text, 80, "> ", "  ", p))
			}
		})
	}
}

// BenchmarkTextWidth measures the grapheme-cluster width scan with ASCII bulk
// paths, CJK, emoji/ZWJ sequences and mixed content.
func BenchmarkTextWidth(b *testing.B) {
	cases := []struct {
		name string
		text string
	}{
		{"ascii-1KB", strings.Repeat("abcdefghij", 100)},
		{"cjk-1KB", strings.Repeat("中文宽度测试", 85)},
		{"emoji-zwj-1KB", strings.Repeat("👨‍👩‍👧‍👦🇨🇳😀", 32)},
		{"mixed-1KB", strings.Repeat("ab中😀e\u0301 ", 64)},
	}
	for _, bc := range cases {
		b.Run(bc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				benchTuiInt = TextWidth(bc.text)
			}
		})
	}
}

// BenchmarkClip measures the cell-bounded, grapheme-safe clipper used by the
// renderer whenever a line must fit the terminal width.
func BenchmarkClip(b *testing.B) {
	cases := []struct {
		name string
		text string
		max  int
	}{
		{"ascii-1KB", strings.Repeat("abcdefghij", 100), 60},
		{"cjk-1KB", strings.Repeat("中文宽度测试", 85), 60},
		{"emoji-zwj-1KB", strings.Repeat("👨‍👩‍👧‍👦🇨🇳😀", 32), 60},
		{"mixed-1KB", strings.Repeat("ab中😀e\u0301 ", 64), 60},
	}
	for _, bc := range cases {
		b.Run(bc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				benchTuiString = Clip(bc.text, bc.max)
			}
		})
	}
}

// BenchmarkWrap measures line breaking, including the CJK no-space path where
// the hard break dominates and the ZWJ-break avoidance path.
func BenchmarkWrap(b *testing.B) {
	cases := []struct {
		name  string
		text  string
		width int
	}{
		{"ascii-spaces", strings.Repeat("the quick brown fox jumps ", 40), 40},
		{"cjk-nospace", strings.Repeat("中文没有空格需要硬换行", 40), 40},
		{"emoji-zwj", strings.Repeat("👨‍👩‍👧‍👦😀🚀", 40), 12},
		{"mixed-long", strings.Repeat("ab中😀e\u0301 ", 60), 30},
	}
	for _, bc := range cases {
		b.Run(bc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				benchTuiLines = len(Wrap(bc.text, bc.width))
			}
		})
	}
}

// benchDiffText builds two texts sharing a fixed prefix and suffix with a
// changed middle region, so the LCS middle path is exercised.
func benchDiffText(n int) (string, string) {
	var oldB, newB strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&oldB, "shared prefix line %d\n", i)
		fmt.Fprintf(&newB, "shared prefix line %d\n", i)
	}
	for i := 0; i < n/4; i++ {
		fmt.Fprintf(&oldB, "old changed line %d\n", i)
		fmt.Fprintf(&newB, "new changed line %d\n", i)
	}
	for i := 0; i < n; i++ {
		fmt.Fprintf(&oldB, "shared suffix line %d\n", i)
		fmt.Fprintf(&newB, "shared suffix line %d\n", i)
	}
	return oldB.String(), newB.String()
}

// BenchmarkDiffLines measures the line diff used for tool output previews and
// buffer change rendering, across a few history sizes.
func BenchmarkDiffLines(b *testing.B) {
	for _, n := range []int{20, 100, 500} {
		b.Run(fmt.Sprintf("lines=%d", n), func(b *testing.B) {
			oldText, newText := benchDiffText(n)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				benchTuiDiffs = DiffLines(oldText, newText)
			}
		})
	}
}
