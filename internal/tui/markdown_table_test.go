package tui

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func tableRender(lines []string, width int) []string {
	return renderMarkdownTable(lines, width, TextWidth, Wrap)
}

func assertTableRows(t *testing.T, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("renderMarkdownTable() =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestRenderMarkdownTableStandard(t *testing.T) {
	lines := []string{
		"| Name | Lang |",
		"| --- | --- |",
		"| argent | Go |",
		"| old | Rust |",
	}
	want := []string{
		"| Name   | Lang |",
		"| ------ | ---- |",
		"| argent | Go   |",
		"| old    | Rust |",
	}
	got := tableRender(lines, 80)
	assertTableRows(t, got, want)

	// Same input must produce byte-identical output on every call.
	assertTableRows(t, tableRender(lines, 80), want)
}

func TestRenderMarkdownTableAlignmentMarkers(t *testing.T) {
	lines := []string{
		"| left | center | right |",
		"| :--- | :---: | ---: |",
		"| a | bb | ccc |",
	}
	want := []string{
		"| left | center | right |",
		"| :--- | :----: | ----: |",
		"| a    |   bb   |   ccc |",
	}
	assertTableRows(t, tableRender(lines, 80), want)
}

func TestRenderMarkdownTableNarrowWraps(t *testing.T) {
	lines := []string{
		"| k | v |",
		"| --- | --- |",
		"| name | alpha beta gamma |",
	}
	got := tableRender(lines, 16)
	want := []string{
		"| k    | v     |",
		"| ---- | ----- |",
		"| name | alpha |",
		"|      | beta  |",
		"|      | gamm  |",
		"|      | a     |",
	}
	assertTableRows(t, got, want)
	for _, row := range got {
		if w := TextWidth(row); w > 16 {
			t.Fatalf("row %q is %d cells wide, want <= 16", row, w)
		}
	}

	// Even an impossibly narrow width stays within the requested budget by
	// shrinking every column to its one-cell minimum.
	tiny := tableRender(lines, 10)
	for _, row := range tiny {
		if w := TextWidth(row); w > 10 {
			t.Fatalf("row %q is %d cells wide, want <= 10", row, w)
		}
	}
}

func TestRenderMarkdownTableTruncatesUnwrappableCell(t *testing.T) {
	lines := []string{
		"| x |",
		"| --- |",
		"| 中文 |",
	}
	got := tableRender(lines, 5)
	want := []string{
		"| x |",
		"| - |",
		"| … |",
		"| … |",
	}
	assertTableRows(t, got, want)
	for _, row := range got {
		if w := TextWidth(row); w > 5 {
			t.Fatalf("row %q is %d cells wide, want <= 5", row, w)
		}
	}
}

func TestRenderMarkdownTableNonTableLinesPassThrough(t *testing.T) {
	lines := []string{
		"intro text",
		"a | b",
		"|--- not a delimiter |",
		"outro text",
	}
	assertTableRows(t, tableRender(lines, 80), lines)

	// A real table block must not swallow the plain line that follows it.
	mixed := []string{
		"| a | b |",
		"|:--- |---:|",
		"| 1 | 2 |",
		"plain after table",
	}
	want := []string{
		"| a | b |",
		"| : | : |",
		"| 1 | 2 |",
		"plain after table",
	}
	assertTableRows(t, tableRender(mixed, 80), want)
}

func TestRenderMarkdownTableUTF8Cells(t *testing.T) {
	lines := []string{
		"| 名字 | 值 |",
		"| --- | --- |",
		"| 中文 | ok |",
	}
	want := []string{
		"| 名字 | 值 |",
		"| ---- | -- |",
		"| 中文 | ok |",
	}
	assertTableRows(t, tableRender(lines, 40), want)
	for _, row := range want {
		if w := TextWidth(row); w != 13 {
			t.Fatalf("row %q measures %d cells, want 13", row, w)
		}
	}
}

func TestRenderMarkdownTableMissingSeparator(t *testing.T) {
	lines := []string{
		"| a | b |",
		"| c | d |",
		"| e | f |",
	}
	assertTableRows(t, tableRender(lines, 80), lines)
}

func TestRenderMarkdownTableUsesInjectedFunctions(t *testing.T) {
	// Rune-count measurement makes each Chinese character one cell instead of
	// the two cells TextWidth reports, proving the injected function is used.
	measure := func(s string) int { return utf8.RuneCountInString(s) }
	breaker := func(s string, width int) []string {
		runes := []rune(s)
		var parts []string
		for len(runes) > width {
			parts = append(parts, string(runes[:width]))
			runes = runes[width:]
		}
		return append(parts, string(runes))
	}
	lines := []string{
		"| 名字 | 值 |",
		"| --- | --- |",
		"| 中文 | ok |",
	}
	want := []string{
		"| 名字 | 值  |",
		"| -- | -- |",
		"| 中文 | ok |",
	}
	got := renderMarkdownTable(lines, 80, measure, breaker)
	assertTableRows(t, got, want)

	// The injected breaker decides where a too-wide cell is split.
	long := []string{
		"| k |",
		"| --- |",
		"| abcdef |",
	}
	longWant := []string{
		"| k    |",
		"| ---- |",
		"| abcd |",
		"| ef   |",
	}
	assertTableRows(t, renderMarkdownTable(long, 8, measure, breaker), longWant)
}

func TestRenderMarkdownTableDeterministicAcrossBlocks(t *testing.T) {
	lines := []string{
		"| one | two |",
		"| --- | --- |",
		"| a   | b   |",
		"between blocks",
		"| x |",
		"| :--: |",
		"| 中 |",
	}
	first := tableRender(lines, 30)
	second := tableRender(lines, 30)
	assertTableRows(t, first, second)
}
