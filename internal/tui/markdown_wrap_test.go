package tui

import (
	"reflect"
	"strings"
	"testing"
)

// wrapStyled wraps the transcript's styled spans. Its breaks must land on
// grapheme boundaries, the same rule TextWidth/Clip/Wrap use, so a multi-rune
// emoji is never cut in half (the input box already honoured this rule).
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
			// No output row may contain a dangling ZWJ/modifier that would
			// render as a broken glyph. Reconstructing the input is the real
			// invariant: wrapping must not add or drop any rune.
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

// Ordinary text must wrap exactly as before; only grapheme clusters change.
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

// markdownLines drives wrapStyled for normal transcript paragraphs; the emoji
// must survive the full path, not just the helper.
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

// A markdown table must honour the same prefix-adjusted content budget as an
// ordinary transcript line. markdownLines prepends the "◆ " assistant mark (or
// its indent) to every rendered row, so handing the full body width to the
// table renderer used to push the right border past the transcript edge.
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
	// The block is still a rendered table (divider present), not dropped text.
	var joined strings.Builder
	for _, line := range lines {
		joined.WriteString(line.Text())
	}
	if !strings.Contains(joined.String(), "---") {
		t.Fatalf("table divider missing from rendered rows: %q", joined.String())
	}
}
