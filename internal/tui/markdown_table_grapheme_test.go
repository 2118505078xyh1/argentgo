package tui

import (
	"strings"
	"testing"
)

// The table cell truncation helpers are the last width path in the package
// that still walks runes one by one. Everywhere else (TextWidth, Clip, Wrap,
// Buffer.WriteString, wrapInput) measures grapheme clusters so a ZWJ emoji, a
// flag or a combining sequence is never left with a dangling joiner or half a
// flag at the cut. These tests pin the same rule for table cells.

func TestTruncateTableCellKeepsZWJClusterIntact(t *testing.T) {
	family := familyZWJ // 👨‍👩‍👧, one 2-cell cluster
	text := family + "中"
	// 4 cells total; a 3-cell budget has room for the whole family but not
	// for the following CJK rune. A run-wise loop stops after the first
	// pictograph and leaves a dangling ZWJ before the ellipsis.
	got := truncateTableCell(text, 3, TextWidth)
	want := family + "…"
	if got != want {
		t.Fatalf("truncateTableCell(%q, 3) = %q, want %q", text, got, want)
	}
	if strings.Contains(strings.TrimSuffix(got, "…"), "\u200d") &&
		!strings.HasSuffix(strings.TrimSuffix(got, "…"), family) {
		t.Fatalf("cut left a dangling ZWJ: %q", got)
	}
}

func TestClipTableCellDropsWholeFlag(t *testing.T) {
	text := flagCN + "中" // flag (2 cells) + CJK (2 cells)
	// A one-cell column cannot hold the two-cell flag. The whole cluster must
	// be dropped instead of clipping after the first regional indicator.
	got := clipTableCell(text, 1, TextWidth)
	if got != "" {
		t.Fatalf("clipTableCell(%q, 1) = %q, want empty (no half a flag)", text, got)
	}
}

func TestRenderMarkdownTableKeepsGraphemesWithSpaceOnlyBreaker(t *testing.T) {
	// A caller may inject a breaker that only splits on whitespace and does
	// not hard-break a long token. With such a breaker the whole cell reaches
	// truncateTableCell, which must still cut on grapheme boundaries.
	breaker := func(s string, width int) []string { return strings.Fields(s) }
	family := familyZWJ
	lines := []string{
		"| h |",
		"| --- |",
		"| " + family + "中 |",
	}
	got := renderMarkdownTable(lines, 7, TextWidth, breaker)
	want := []string{
		"| h   |",
		"| --- |",
		"| " + family + "… |",
	}
	wantRows := strings.Join(want, "\n")
	if rows := strings.Join(got, "\n"); rows != wantRows {
		t.Fatalf("renderMarkdownTable() =\n%s\nwant\n%s", rows, wantRows)
	}
	for _, row := range got {
		if strings.Contains(strings.ReplaceAll(row, family, ""), "\u200d") {
			t.Fatalf("row %q has a dangling ZWJ", row)
		}
	}
}
