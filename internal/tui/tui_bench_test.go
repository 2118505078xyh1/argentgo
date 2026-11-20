package tui

import (
	"fmt"
	"strings"
	"testing"
)

// 本文件记录 TUI 每帧热路径的性能基线：
// markdown 渲染、感知字素的宽度/裁剪/换行和行差异。夹具
// 是固定的字符串；不涉及终端、网络或真实墙上时钟
// 依赖。不断言任何墙上时钟阈值。

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

// benchMarkdownText 构建一个约 n 行的 markdown 块，混合标题、
// 项目符号、行内代码、围栏块和表格。
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

// BenchmarkMarkdownLines 测量用于每个助手块的渲染器。
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

// BenchmarkTextWidth 测量感知字素簇的宽度扫描，覆盖 ASCII 批量
// 路径、CJK、emoji/ZWJ 序列和混合内容。
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

// BenchmarkClip 测量渲染器在行必须适配终端宽度时使用的
// 按单元格限界、字素安全的裁剪器。
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

// BenchmarkWrap 测量断行，包括 CJK 无空格路径（硬断占主导）
// 和避免 ZWJ 断开的路径。
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

// benchDiffText 构建两段共享固定前缀和后缀、
// 中间区域不同的文本，使 LCS 中间路径被覆盖。
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

// BenchmarkDiffLines 测量用于工具输出预览和
// 缓冲区变化渲染的行差异，覆盖几种历史大小。
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
