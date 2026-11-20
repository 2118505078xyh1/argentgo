package tui

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// withStructuredToolResults runs f with the opt-in switch set to want and
// always restores the previous value, keeping the package-level default intact
// for the rest of the suite.
func withStructuredToolResults(t *testing.T, want bool, f func()) {
	t.Helper()
	old := StructuredToolResults
	StructuredToolResults = want
	defer func() { StructuredToolResults = old }()
	f()
}

func TestStructuredToolResultsDefaultOffKeepsLegacyRendering(t *testing.T) {
	withStructuredToolResults(t, false, func() {
		p, _ := paletteFor(ThemeDark)
		entry := Entry{Kind: EntryTool, Name: "exec", Args: `{}`, Output: `{"a":1,"b":2}`}

		got := lineTexts(toolLines(entry, 60, false, p))
		// The original preview keeps compact JSON on a single clipped row and
		// uses the tool palette style — byte-for-byte the legacy output.
		want := strings.Join([]string{"● exec()", `  {"a":1,"b":2}`}, "\n")
		if got != want {
			t.Fatalf("default (off) rendering changed:\n got: %q\nwant: %q", got, want)
		}
	})
}

func TestStructuredToolResultsOffIsProcessWideDefault(t *testing.T) {
	if StructuredToolResults {
		t.Fatal("StructuredToolResults must default to false; default behaviour changed")
	}
}

func TestStructuredToolResultsOnPrettyPrintsJSON(t *testing.T) {
	withStructuredToolResults(t, true, func() {
		p, _ := paletteFor(ThemeDark)
		entry := Entry{Kind: EntryTool, Name: "exec", Args: `{}`, Output: `{"a":1,"b":2}`}

		got := lineTexts(toolLines(entry, 60, false, p))
		want := strings.Join([]string{
			"● exec()",
			"  {",
			`    "a": 1,`,
			"  … 2 more lines",
		}, "\n")
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("structured JSON rendering:\n got: %q\nwant: %q", got, want)
		}
	})
}

func TestStructuredToolResultsOnPreservesDiff(t *testing.T) {
	withStructuredToolResults(t, true, func() {
		p, _ := paletteFor(ThemeDark)
		raw := strings.Join([]string{
			"--- a/f.txt",
			"+++ b/f.txt",
			"@@ -1 +1 @@",
			"-old",
			"+new",
		}, "\n")
		entry := Entry{Kind: EntryTool, Name: "shell", Args: `{}`, Output: raw}

		collapsed := lineTexts(toolLines(entry, 60, false, p))
		for _, want := range []string{"  --- a/f.txt", "  +++ b/f.txt", "  … 3 more lines"} {
			if !strings.Contains(collapsed, want) {
				t.Fatalf("collapsed diff is missing %q:\n%s", want, collapsed)
			}
		}

		expanded := lineTexts(toolLines(entry, 60, true, p))
		for _, want := range []string{"  --- a/f.txt", "  +++ b/f.txt", "  @@ -1 +1 @@", "  -old", "  +new"} {
			if !strings.Contains(expanded, want) {
				t.Fatalf("expanded diff is missing %q:\n%s", want, expanded)
			}
		}
		if strings.Contains(expanded, "more lines") {
			t.Fatalf("expanded short diff should not be capped:\n%s", expanded)
		}
	})
}

func TestStructuredToolResultsOnPlainTextFallback(t *testing.T) {
	withStructuredToolResults(t, true, func() {
		p, _ := paletteFor(ThemeDark)
		entry := Entry{Kind: EntryTool, Name: "exec", Args: `{}`, Output: "first\nsecond\nthird\nfour"}

		got := lineTexts(toolLines(entry, 60, false, p))
		want := strings.Join([]string{
			"● exec()",
			"  first",
			"  second",
			"  … 2 more lines",
		}, "\n")
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("plain-text fallback:\n got: %q\nwant: %q", got, want)
		}
	})
}

func TestStructuredToolResultsOnKeepsCJKIntact(t *testing.T) {
	withStructuredToolResults(t, true, func() {
		p, _ := paletteFor(ThemeDark)
		entry := Entry{Kind: EntryTool, Name: "exec", Args: `{}`, Output: "汉字😀\nsecond 中文"}

		got := lineTexts(toolLines(entry, 60, false, p))
		for _, want := range []string{"  汉字😀", "  second 中文"} {
			if !strings.Contains(got, want) {
				t.Fatalf("CJK row %q is missing:\n%s", want, got)
			}
		}
		if !utf8.ValidString(got) {
			t.Fatalf("rendering split a UTF-8 rune: %q", got)
		}
	})
}

func TestStructuredToolResultsDoesNotAffectEditDiffPreview(t *testing.T) {
	// The edit/multi_edit diff preview takes an early return in toolLines and
	// must be identical whether the structured switch is on or off.
	p, _ := paletteFor(ThemeDark)
	entry := Entry{
		Kind: EntryTool,
		Name: "edit",
		Args: `{"path":"f.go","old_text":"alpha\nbeta","new_text":"alpha\ngamma"}`,
	}
	off := withStructuredResultText(t, false, entry, p)
	on := withStructuredResultText(t, true, entry, p)
	if off != on {
		t.Fatalf("edit diff preview changed with the switch:\n off: %q\n  on: %q", off, on)
	}
}

func withStructuredResultText(t *testing.T, structured bool, entry Entry, p Palette) string {
	t.Helper()
	old := StructuredToolResults
	StructuredToolResults = structured
	defer func() { StructuredToolResults = old }()
	return lineTexts(toolLines(entry, 60, false, p))
}
