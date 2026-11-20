package tui

import "testing"

// The input box uses the same grapheme-boundary rule as the transcript: a ZWJ
// family emoji, a regional-indicator flag and a combining sequence are single
// visible glyphs and must never be folded across two rows. wrapInput used to
// measure each rune on its own, so the second pictograph of a ZWJ sequence (or
// the second flag letter) could be pushed onto the next row, leaving a dangling
// joiner / half a flag behind.
func TestWrapInputKeepsGraphemeClustersWhole(t *testing.T) {
	family := "A👨‍👩‍👧B" // A + family emoji + B, 1+2+1 = 4 cells
	rows := wrapInput([]rune(family), 4)
	if len(rows) != 1 {
		t.Fatalf("wrapping %q at 4 cells = %d rows %+v, want one whole row", family, len(rows), rows)
	}
	if rows[0].text != family {
		t.Fatalf("row text = %q, want %q", rows[0].text, family)
	}
}

func TestWrapInputDoesNotSplitFlagPair(t *testing.T) {
	text := "X🇺🇸Y" // X + US flag + Y
	rows := wrapInput([]rune(text), 2)
	if len(rows) != 3 {
		t.Fatalf("wrapping %q at 2 cells = %d rows %+v, want 3 whole rows", text, len(rows), rows)
	}
	want := []string{"X", "🇺🇸", "Y"}
	for i, row := range rows {
		if row.text != want[i] {
			t.Errorf("row %d = %q, want %q", i, row.text, want[i])
		}
	}
}

// Explicit newlines and ordinary wide/CJK text must keep their old behaviour.
func TestWrapInputStillFoldsLinesAndWideRunes(t *testing.T) {
	rows := wrapInput([]rune("你ab\ncd"), 3)
	want := []string{"你a", "b", "cd"}
	if len(rows) != len(want) {
		t.Fatalf("rows = %d %+v, want %d", len(rows), rows, len(want))
	}
	for i, row := range rows {
		if row.text != want[i] {
			t.Errorf("row %d = %q, want %q", i, row.text, want[i])
		}
	}
}
