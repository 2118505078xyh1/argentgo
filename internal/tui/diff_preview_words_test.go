package tui

import (
	"strings"
	"testing"
)

// spansWithStyle returns the text of every span drawn with style.
func spansWithStyle(line Line, style Style) []string {
	var out []string
	for _, span := range line.Spans {
		if span.Style == style {
			out = append(out, span.Text)
		}
	}
	return out
}

func containsSpan(texts []string, want string) bool {
	for _, text := range texts {
		if text == want {
			return true
		}
	}
	return false
}

func testPalette(t *testing.T) Palette {
	t.Helper()
	p, err := paletteFor(ThemeDark)
	if err != nil {
		t.Fatalf("paletteFor: %v", err)
	}
	return p
}

func TestDiffPreviewHighlightsOnlyChangedWords(t *testing.T) {
	p := testPalette(t)
	lines := diffPreviewLines([]DiffLine{
		{Kind: DiffDel, Text: "a = left side"},
		{Kind: DiffAdd, Text: "a = right side"},
	}, 10, 80, p)
	if len(lines) != 2 {
		t.Fatalf("got %d rows, want 2: %s", len(lines), lineTexts(lines))
	}

	if got := lines[0].Text(); got != "  - a = left side" {
		t.Errorf("del row text = %q", got)
	}
	if got := lines[1].Text(); got != "  + a = right side" {
		t.Errorf("add row text = %q", got)
	}

	// Only the edited word carries the strong background; the surrounding text
	// keeps the ordinary failure/tool foreground.
	changedDel, changedAdd := changedWordStyle(p, false), changedWordStyle(p, true)
	if got := spansWithStyle(lines[0], changedDel); !containsSpan(got, "left") {
		t.Errorf("del changed spans = %q, want [left]", got)
	}
	if got := spansWithStyle(lines[1], changedAdd); !containsSpan(got, "right") {
		t.Errorf("add changed spans = %q, want [right]", got)
	}
	if got := spansWithStyle(lines[0], p.failure()); !containsSpan(got, "a = ") || !containsSpan(got, " side") {
		t.Errorf("del unchanged spans = %q, want the original line style", got)
	}
	if got := spansWithStyle(lines[1], p.tool()); !containsSpan(got, "a = ") || !containsSpan(got, " side") {
		t.Errorf("add unchanged spans = %q, want the original line style", got)
	}
	for _, span := range lines[0].Spans {
		if span.Style == changedDel && strings.Contains(span.Text, "side") {
			t.Errorf("unchanged word %q was highlighted as changed", span.Text)
		}
	}
}

func TestDiffPreviewWholeLineReplacementUsesOneChangedSpan(t *testing.T) {
	p := testPalette(t)
	lines := diffPreviewLines([]DiffLine{
		{Kind: DiffDel, Text: "alpha beta gamma"},
		{Kind: DiffAdd, Text: "delta epsilon zeta"},
	}, 10, 80, p)

	if got := spansWithStyle(lines[0], changedWordStyle(p, false)); !containsSpan(got, "alpha beta gamma") {
		t.Errorf("del changed spans = %q, want the whole line", got)
	}
	if got := spansWithStyle(lines[1], changedWordStyle(p, true)); !containsSpan(got, "delta epsilon zeta") {
		t.Errorf("add changed spans = %q, want the whole line", got)
	}
	// A wholesale replacement has no unchanged body segment to keep.
	for _, line := range lines {
		for _, style := range []Style{p.failure(), p.tool()} {
			for _, text := range spansWithStyle(line, style) {
				if text != "- " && text != "+ " {
					t.Errorf("degraded row kept an unchanged body span %q", text)
				}
			}
		}
	}
}

func TestDiffPreviewUnpairedRowsKeepWholeLineStyle(t *testing.T) {
	p := testPalette(t)
	lines := diffPreviewLines([]DiffLine{
		{Kind: DiffDel, Text: "old alone"},
		{Kind: DiffContext, Text: "same"},
		{Kind: DiffAdd, Text: "new alone"},
	}, 10, 80, p)
	if len(lines) != 3 {
		t.Fatalf("got %d rows, want 3: %s", len(lines), lineTexts(lines))
	}

	// Each unpaired row still has exactly one whole-line body span.
	want := [][2]string{
		{"old alone", "- "},
		{"same", "  "},
		{"new alone", "+ "},
	}
	wantStyles := []Style{p.failure(), p.muted(), p.tool()}
	for i, tc := range want {
		if len(lines[i].Spans) != 3 {
			t.Errorf("row %d has %d spans, want 3: %+v", i, len(lines[i].Spans), lines[i].Spans)
			continue
		}
		if got := lines[i].Spans[2]; got.Text != tc[0] || got.Style != wantStyles[i] {
			t.Errorf("row %d body = %+v, want text %q style %+v", i, got, tc[0], wantStyles[i])
		}
		if got := lines[i].Spans[1].Text; got != tc[1] {
			t.Errorf("row %d marker = %q, want %q", i, got, tc[1])
		}
		for _, span := range lines[i].Spans {
			if span.Style == changedWordStyle(p, false) || span.Style == changedWordStyle(p, true) {
				t.Errorf("unpaired row %d got a word-highlight span %+v", i, span)
			}
		}
	}
}

func TestDiffPreviewClipsSegmentsPerCell(t *testing.T) {
	p := testPalette(t)
	diffs := []DiffLine{
		{Kind: DiffDel, Text: "aa bb cc dd ee"},
		{Kind: DiffAdd, Text: "aa bb cc dd FF"},
	}
	cases := []struct {
		name, body string
		width      int
		wantWidth  int
		wantBody   []string
	}{
		{"both segments fit", "aa bb cc dd ", 18, 18, []string{"aa bb cc dd ", "ee"}},
		{"second segment clipped", "e", 17, 17, []string{"aa bb cc dd ", "e"}},
		{"first segment clipped stops the row", "aa bb cc", 12, 12, []string{"aa bb cc"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lines := diffPreviewLines(diffs, 10, tc.width, p)
			line := lines[0]
			if got := line.Width(); got != tc.wantWidth {
				t.Errorf("row width = %d, want %d: %s", got, tc.wantWidth, lineTexts(lines))
			}
			var bodies []string
			for _, span := range line.Spans[2:] {
				bodies = append(bodies, span.Text)
			}
			if strings.Join(bodies, "|") != strings.Join(tc.wantBody, "|") {
				t.Errorf("body spans = %q, want %q", bodies, tc.wantBody)
			}
		})
	}
}

func TestDiffPreviewCJKClipsOnGraphemeBoundary(t *testing.T) {
	p := testPalette(t)
	diffs := []DiffLine{
		{Kind: DiffDel, Text: "计数器等于一"},
		{Kind: DiffAdd, Text: "计数器等于二"},
	}

	wide := diffPreviewLines(diffs, 10, 16, p)
	if got := spansWithStyle(wide[0], changedWordStyle(p, false)); !containsSpan(got, "一") {
		t.Errorf("CJK changed spans = %q, want [一]", got)
	}
	if got := wide[0].Width(); got != 16 {
		t.Errorf("CJK row width = %d, want 16", got)
	}

	// With no room for a full two-cell ideograph, the changed word is dropped
	// whole instead of being cut into an invalid fragment.
	narrow := diffPreviewLines(diffs, 10, 15, p)[0]
	if got := narrow.Width(); got > 15 {
		t.Errorf("narrow CJK row width = %d, want <= 15", got)
	}
	if got := spansWithStyle(narrow, changedWordStyle(p, false)); len(got) != 0 {
		t.Errorf("narrow CJK row highlighted a partial word: %q", got)
	}
	for _, span := range narrow.Spans {
		if !strings.Contains(span.Text, "\xef\xbf\xbd") {
			continue
		}
		t.Errorf("CJK row contains a replacement rune: %q", span.Text)
	}
}

func TestDiffPreviewRowCapMatchesBaseline(t *testing.T) {
	p := testPalette(t)
	var many []DiffLine
	for i := 0; i < 10; i++ {
		many = append(many, DiffLine{Kind: DiffAdd, Text: "row"})
	}
	lines := diffPreviewLines(many, 3, 40, p)
	if len(lines) != 4 {
		t.Fatalf("got %d rows, want 3 + overflow: %s", len(lines), lineTexts(lines))
	}
	if got := lines[3].Text(); got != "  … 7 more lines" {
		t.Errorf("overflow marker = %q", got)
	}

	// A pair is only formed when both rows fit; otherwise the leading deletion
	// falls back to the whole-line path and the addition counts as overflow.
	boundary := []DiffLine{
		{Kind: DiffContext, Text: "keep"},
		{Kind: DiffDel, Text: "left"},
		{Kind: DiffAdd, Text: "right"},
	}
	lines = diffPreviewLines(boundary, 2, 40, p)
	if len(lines) != 3 {
		t.Fatalf("got %d rows, want 2 + overflow: %s", len(lines), lineTexts(lines))
	}
	if got := spansWithStyle(lines[1], changedWordStyle(p, false)); len(got) != 0 {
		t.Errorf("deletion at the row boundary was word-highlighted: %q", got)
	}
	if got := lines[2].Text(); got != "  … 1 more lines" {
		t.Errorf("overflow marker = %q", got)
	}
}
