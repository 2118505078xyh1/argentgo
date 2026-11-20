package tui

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFormatToolResultContentPrettyPrintsJSONObject(t *testing.T) {
	raw := `{"name":"argo","n":2,"ok":true}`
	want := []string{
		"{",
		`  "name": "argo",`,
		`  "n": 2,`,
		`  "ok": true`,
		"}",
	}
	if got := formatToolResultContent(raw, 20); !reflect.DeepEqual(got, want) {
		t.Fatalf("JSON object formatted as %q, want %q", got, want)
	}
}

func TestFormatToolResultContentPrettyPrintsJSONArray(t *testing.T) {
	raw := `[1,2,3]`
	want := []string{"[", "  1,", "  2,", "  3", "]"}
	if got := formatToolResultContent(raw, 20); !reflect.DeepEqual(got, want) {
		t.Fatalf("JSON array formatted as %q, want %q", got, want)
	}
}

func TestFormatToolResultContentDetectsUnifiedDiff(t *testing.T) {
	raw := strings.Join([]string{
		"--- a/f.txt",
		"+++ b/f.txt",
		"@@ -1 +1 @@",
		"-old",
		"+new",
		" same",
	}, "\n")
	got := formatToolResultContent(raw, 20)
	want := []string{
		"--- a/f.txt",
		"+++ b/f.txt",
		"@@ -1 +1 @@",
		"-old",
		"+new",
		" same",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("diff formatted as %q, want %q", got, want)
	}
}

func TestFormatToolResultContentDiffWithoutHunkStillDetected(t *testing.T) {
	raw := "--- a/f.txt\n+++ b/f.txt\n-old\n+new"
	got := formatToolResultContent(raw, 20)
	if len(got) != 4 || got[0] != "--- a/f.txt" || got[3] != "+new" {
		t.Fatalf("two-header diff not preserved line by line: %q", got)
	}
}

func TestFormatToolResultContentFallsBackToPlainText(t *testing.T) {
	raw := "first line\nsecond line\nthird line"
	want := []string{"first line", "second line", "third line"}
	if got := formatToolResultContent(raw, 20); !reflect.DeepEqual(got, want) {
		t.Fatalf("plain text formatted as %q, want %q", got, want)
	}
}

func TestFormatToolResultContentKeepsCJKAndEmojiIntact(t *testing.T) {
	raw := "汉字😀\nsecond " + familyZWJ + " " + flagCN
	want := []string{"汉字😀", "second " + familyZWJ + " " + flagCN}
	got := formatToolResultContent(raw, 20)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CJK/emoji formatted as %q, want %q", got, want)
	}
	for i, line := range got {
		if !utf8.ValidString(line) {
			t.Fatalf("line %d is not valid UTF-8: %q", i, line)
		}
	}
}

func TestFormatToolResultContentHonoursLineLimit(t *testing.T) {
	raw := strings.Join([]string{
		"l0", "l1", "l2", "l3", "l4", "l5", "l6", "l7", "l8", "l9",
	}, "\n")

	if got := formatToolResultContent(raw, 3); len(got) != 3 {
		t.Fatalf("maxLines=3 produced %d rows: %q", len(got), got)
	} else if got[0] != "l0" || got[1] != "l1" || got[2] != "… 8 more lines" {
		t.Fatalf("unexpected capped rows: %q", got)
	}

	if got := formatToolResultContent(raw, 1); len(got) != 1 || got[0] != "l0" {
		t.Fatalf("maxLines=1 produced %q", got)
	}

	if got := formatToolResultContent(raw, 0); len(got) != 0 {
		t.Fatalf("maxLines=0 produced %q", got)
	}

	if got := formatToolResultContent("", 5); len(got) != 0 {
		t.Fatalf("empty raw produced %q", got)
	}
}

func TestFormatToolResultContentFallsBackForInvalidJSON(t *testing.T) {
	for _, raw := range []string{`{"a":`, `not json {`, `{"a":1} trailing`} {
		got := formatToolResultContent(raw, 20)
		if !reflect.DeepEqual(got, []string{raw}) {
			t.Fatalf("invalid JSON %q formatted as %q, want a single plain row", raw, got)
		}
	}
}

func TestFormatToolResultContentScalarJSONUsesPlainPath(t *testing.T) {
	// A valid JSON scalar is not an object/array: keep it as plain text.
	raw := `"just a string"`
	if got := formatToolResultContent(raw, 20); !reflect.DeepEqual(got, []string{raw}) {
		t.Fatalf("JSON scalar formatted as %q, want %q", got, []string{raw})
	}
}

func TestFormatToolResultContentPreservesANSI(t *testing.T) {
	raw := "\x1b[31mred\x1b[0m\n\x1b[32mgreen\x1b[0m"
	want := []string{"\x1b[31mred\x1b[0m", "\x1b[32mgreen\x1b[0m"}
	got := formatToolResultContent(raw, 20)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ANSI output = %q, want %q", got, want)
	}
}

func TestFormatToolResultContentIgnoresMarkdownHorizontalRule(t *testing.T) {
	// A lone "---" is a markdown rule, not a unified diff header pair.
	raw := "intro\n---\noutro"
	want := []string{"intro", "---", "outro"}
	if got := formatToolResultContent(raw, 20); !reflect.DeepEqual(got, want) {
		t.Fatalf("markdown rule formatted as %q, want %q", got, want)
	}
}

func TestFormatToolResultContentNormalisesCRLF(t *testing.T) {
	got := formatToolResultContent("a\r\nb\r\n", 20)
	want := []string{"a", "b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CRLF input formatted as %q, want %q", got, want)
	}
}

func TestFormatToolResultContentAppliesLimitToJSON(t *testing.T) {
	raw := `{"a":1,"b":2,"c":3,"d":4}`
	got := formatToolResultContent(raw, 3)
	if len(got) != 3 {
		t.Fatalf("JSON with maxLines=3 produced %d rows: %q", len(got), got)
	}
	if got[2] != "… 4 more lines" {
		t.Fatalf("JSON cap marker = %q, want %q", got[2], "… 4 more lines")
	}
}
