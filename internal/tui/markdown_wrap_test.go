package tui

import (
	"reflect"
	"strings"
	"testing"
)

// wrapStyled 对记录的带样式 span 换行。其断点必须落在
// 字素边界上，与 TextWidth/Clip/Wrap 使用相同的规则，使多 rune
// emoji 绝不会被切成两半（输入框已经遵守此规则）。
func TestWrapStyledKeepsGraphemeClustersIntact(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		width int
		want  []string
	}{
		{
			name:  "ZWJ family stays whole",
			text:  "A" + familyZWJ + "B",
			width: 3,
			want:  []string{"A" + familyZWJ, "B"},
		},
		{
			name:  "regional-indicator flag stays whole",
			text:  flagCN + "ab",
			width: 3,
			want:  []string{flagCN + "a", "b"},
		},
		{
			name:  "combining sequence stays with its base",
			text:  combiningE + "abc",
			width: 2,
			want:  []string{combiningE + "a", "bc"},
		},
		{
			name:  "skin-tone modifier stays with its emoji",
			text:  thumbTone + "xy",
			width: 3,
			want:  []string{thumbTone + "x", "y"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lines := wrapStyled([]Span{{Text: tc.text}}, tc.width, "", "")
			got := make([]string, len(lines))
			for i, line := range lines {
				got[i] = line.Text()
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("wrapStyled(%q, %d) = %q, want %q", tc.text, tc.width, got, tc.want)
			}
			// 任何输出行都不得包含会渲染成损坏字形的悬空 ZWJ/修饰符。
			// 重建输入是真正的不变量：
			// 换行不得添加或丢弃任何 rune。
			if joined := strings.Join(got, ""); joined != tc.text {
				t.Fatalf("wrapped rows reconstruct to %q, want %q", joined, tc.text)
			}
		})
	}
}

func TestWrapStyledPreservesStylesAcrossClusters(t *testing.T) {
	bold := Style{Bold: true}
	red := Style{Fg: ColorRed}
	lines := wrapStyled([]Span{
		{Text: "ab", Style: bold},
		{Text: combiningE + "d", Style: red},
	}, 12, "", "")
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1: %v", len(lines), lines)
	}
	line := lines[0]
	if got := line.Text(); got != "ab"+combiningE+"d" {
		t.Fatalf("line text = %q", got)
	}
	if len(line.Spans) != 2 {
		t.Fatalf("got %d spans, want 2: %+v", len(line.Spans), line.Spans)
	}
	if line.Spans[0].Text != "ab" || line.Spans[0].Style != bold {
		t.Errorf("first span = %+v, want {ab %+v}", line.Spans[0], bold)
	}
	if line.Spans[1].Text != combiningE+"d" || line.Spans[1].Style != red {
		t.Errorf("second span = %+v, want {%q %+v}", line.Spans[1], combiningE+"d", red)
	}
}

// 普通文本必须像以前一样换行；只有字素簇改变。
func TestWrapStyledASCIIWrapUnchanged(t *testing.T) {
	lines := wrapStyled([]Span{{Text: "alpha beta"}}, 5, "> ", "  ")
	got := make([]string, len(lines))
	for i, line := range lines {
		got[i] = line.Text()
	}
	want := []string{"> alp", "  ha", "  bet", "  a"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("wrapStyled = %q, want %q", got, want)
	}
}

// markdownLines 为普通记录段落驱动 wrapStyled；emoji
// 必须走完整路径，而不只是辅助函数。
func TestMarkdownLinesKeepZWJEmojiIntact(t *testing.T) {
	p, _ := paletteFor(ThemeDark)
	lines := markdownLines("A"+familyZWJ+"B", 3, "", "", p)
	var joined strings.Builder
	for _, line := range lines {
		joined.WriteString(line.Text())
	}
	if got := joined.String(); got != "A"+familyZWJ+"B" {
		t.Fatalf("markdownLines reconstructed %q", got)
	}
	for _, line := range lines {
		for _, span := range line.Spans {
			if strings.Contains(span.Text, "\u200d") && !strings.Contains(span.Text, familyZWJ) {
				t.Fatalf("a ZWJ cluster was split: %q", span.Text)
			}
		}
	}
}

// markdown 表格必须遵守与普通记录行相同的、按前缀调整的内容预算。
// markdownLines 会给每个渲染行前置 "◆ " 助手标记（或
// 其缩进），因此把完整正文宽度交给
// 表格渲染器过去会把右边框推过记录边缘。
func TestMarkdownLinesTableRespectsPrefixWidth(t *testing.T) {
	p, _ := paletteFor(ThemeDark)
	const width = 20
	md := "| aaaaaaaaaaa | bbbbbbbbbbb |\n| --- | --- |\n| ccccccccccc | ddddddddddd |"
	lines := markdownLines(md, width, "◆ ", "  ", p)
	if len(lines) == 0 {
		t.Fatal("expected table rows, got none")
	}
	for i, line := range lines {
		if w := line.Width(); w > width {
			t.Errorf("row %d is %d cells wide, want <= %d: %q", i, w, width, line.Text())
		}
	}
	if got := lines[0].Spans[0].Text; got != "◆ " {
		t.Fatalf("first row prefix = %q, want the assistant mark", got)
	}
	// 该块仍是渲染好的表格（有分隔线），而不是被丢弃的文本。
	var joined strings.Builder
	for _, line := range lines {
		joined.WriteString(line.Text())
	}
	if !strings.Contains(joined.String(), "---") {
		t.Fatalf("table divider missing from rendered rows: %q", joined.String())
	}
}
