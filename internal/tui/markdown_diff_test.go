package tui

import (
	"fmt"
	"strings"
	"testing"
)

func lineTexts(lines []Line) string {
	parts := make([]string, 0, len(lines))
	for _, line := range lines {
		parts = append(parts, line.Text())
	}
	return strings.Join(parts, "\n")
}

func TestEditDiffUsesCollapsedAndExpandedRowBudgets(t *testing.T) {
	const total = 40
	newLines := []string{"line 0"}
	for i := 1; i < total; i++ {
		newLines = append(newLines, fmt.Sprintf("added %d", i))
	}
	args := fmt.Sprintf(`{"path":"f.go","old":"line 0","new":%q}`, strings.Join(newLines, "\n"))
	entry := Entry{Kind: EntryTool, Name: "edit", Args: args}
	p, _ := paletteFor(ThemeDark)

	collapsed := lineTexts(toolLines(entry, 60, false, p))
	for _, want := range []string{"+ added 1", "+ added 2", "… 37 more lines"} {
		if !strings.Contains(collapsed, want) {
			t.Fatalf("collapsed diff is missing %q:\n%s", want, collapsed)
		}
	}
	if strings.Contains(collapsed, "added 39") {
		t.Fatalf("collapsed diff leaked a row past the 3-line cap:\n%s", collapsed)
	}

	expanded := lineTexts(toolLines(entry, 60, true, p))
	if strings.Contains(expanded, "… 37 more lines") {
		t.Fatalf("expanded diff still reports the collapsed remainder:\n%s", expanded)
	}
	if !strings.Contains(expanded, "… 20 more lines") {
		t.Fatalf("expanded diff should use the 20-row cap:\n%s", expanded)
	}
}

func TestDiffLinesMarksChangedRows(t *testing.T) {
	got := DiffLines("keep\nbefore\nkeep2", "keep\nafter\nkeep2")
	want := []DiffLine{
		{Kind: DiffContext, Text: "keep"},
		{Kind: DiffDel, Text: "before"},
		{Kind: DiffAdd, Text: "after"},
		{Kind: DiffContext, Text: "keep2"},
	}
	if len(got) != len(want) {
		t.Fatalf("DiffLines returned %d rows, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestEditDiffRendersPlusAndMinusRows(t *testing.T) {
	p, _ := paletteFor(ThemeDark)
	entry := Entry{
		Kind: EntryTool,
		Name: "edit",
		Args: `{"path":"f.go","old_text":"alpha\nbeta","new_text":"alpha\ngamma"}`,
	}
	got := lineTexts(toolLines(entry, 60, false, p))
	for _, want := range []string{"alpha", "- beta", "+ gamma"} {
		if !strings.Contains(got, want) {
			t.Fatalf("diff is missing %q:\n%s", want, got)
		}
	}

	// A small diff fits under both caps: expanding shows every row and drops
	// the overflow marker entirely.
	expanded := lineTexts(toolLines(entry, 60, true, p))
	if strings.Contains(expanded, "more lines") {
		t.Fatalf("a short diff should not be truncated when expanded:\n%s", expanded)
	}
}

func TestNonEditToolRenderingIsUnchanged(t *testing.T) {
	p, _ := paletteFor(ThemeDark)
	entry := Entry{Kind: EntryTool, Name: "read", Args: `{"path":"f.go"}`, Output: "one\ntwo\nthree\nfour"}
	got := lineTexts(toolLines(entry, 60, false, p))
	if strings.Contains(got, "+ ") || strings.Contains(got, "- ") {
		t.Fatalf("a non-edit tool was rendered as a diff:\n%s", got)
	}
	if !strings.Contains(got, "… 1 more lines") {
		t.Fatalf("the ordinary output preview changed:\n%s", got)
	}
}
