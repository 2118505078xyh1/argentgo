package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runEditTool calls the registered edit tool with yolo enabled so the mutation
// gate does not interfere with these filesystem assertions.
func runEditTool(t *testing.T, root, rel, old, newText string, repair bool) (string, error) {
	t.Helper()
	args := map[string]any{
		"path":   rel,
		"old":    old,
		"new":    newText,
		"repair": repair,
	}
	return runToolArgs(t, root, "edit", args)
}

func runToolArgs(t *testing.T, root, name string, args map[string]any) (string, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return New(root, true).Run(context.Background(), name, string(raw))
}

func writeFixture(t *testing.T, root, rel, content string) string {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func readFixture(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestRepairReplacePrefersExactLiteralMatch(t *testing.T) {
	// The first block is an exact, unique match; the second only matches after
	// stripping its extra indent. Rung 1 must win over the fuzzy rung.
	got, ok := repairReplace("foo\nbar\n    foo\n    bar\n", "foo\nbar", "X\nY")
	if !ok {
		t.Fatal("repairReplace did not find the exact match")
	}
	if want := "X\nY\n    foo\n    bar\n"; got != want {
		t.Fatalf("result = %q, want %q", got, want)
	}
}

func TestEditRepairExactMatchStillWins(t *testing.T) {
	root := t.TempDir()
	path := writeFixture(t, root, "f.txt", "unique\n    padded\n")

	out, err := runEditTool(t, root, "f.txt", "unique", "FIRST", true)
	if err != nil {
		t.Fatalf("edit repair=true: %v", err)
	}
	if out != "edited f.txt" {
		t.Fatalf("out = %q, want %q", out, "edited f.txt")
	}
	if got, want := readFixture(t, path), "FIRST\n    padded\n"; got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestEditRepairReappliesOriginalIndentation(t *testing.T) {
	root := t.TempDir()
	path := writeFixture(t, root, "f.txt", "\tfoo()\n\tbar()\n")

	// The old/new blocks use spaces; the file uses tabs. The literal is absent.
	got, ok := repairReplace("\tfoo()\n\tbar()\n", "    foo()\n    bar()", "    foo()\n    baz()")
	if !ok {
		t.Fatal("indentation-only mismatch was not repaired")
	}
	if want := "\tfoo()\n\tbaz()\n"; got != want {
		t.Fatalf("repairReplace = %q, want %q", got, want)
	}

	out, err := runEditTool(t, root, "f.txt", "    foo()\n    bar()", "    foo()\n    baz()", true)
	if err != nil {
		t.Fatalf("edit repair=true: %v", err)
	}
	if out != "edited f.txt" {
		t.Fatalf("out = %q", out)
	}
	if content := readFixture(t, path); content != "\tfoo()\n\tbaz()\n" {
		t.Fatalf("content = %q, want original tab indent preserved", content)
	}
}

func TestEditRepairDropsSpuriousBlankLine(t *testing.T) {
	root := t.TempDir()
	path := writeFixture(t, root, "f.txt", "alpha\nbeta\ngamma\n")

	// Leading blank line in both old and new.
	out, err := runEditTool(t, root, "f.txt", "\nalpha\nbeta\n", "\nalpha\nBETA\n", true)
	if err != nil {
		t.Fatalf("leading blank repair: %v", err)
	}
	if out != "edited f.txt" {
		t.Fatalf("out = %q", out)
	}
	if got, want := readFixture(t, path), "alpha\nBETA\ngamma\n"; got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}

	// Trailing blank line in both old and new.
	path = writeFixture(t, root, "g.txt", "alpha\nbeta\ngamma\n")
	if _, err := runEditTool(t, root, "g.txt", "beta\ngamma\n\n", "BETA\ngamma\n\n", true); err != nil {
		t.Fatalf("trailing blank repair: %v", err)
	}
	if got, want := readFixture(t, path), "alpha\nBETA\ngamma\n"; got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestEditRepairElisionKeepsUniqueAnchorsAndElidedText(t *testing.T) {
	content := "header\nstart\nMIDDLE DONT TOUCH\nend\nfooter\n"
	got, ok := repairReplace(content, "start\n...\nend\n", "begin\n...\nfinish\n")
	if !ok {
		t.Fatal("elision repair did not match")
	}
	want := "header\nbegin\nMIDDLE DONT TOUCH\nfinish\nfooter\n"
	if got != want {
		t.Fatalf("result = %q, want %q", got, want)
	}

	root := t.TempDir()
	path := writeFixture(t, root, "f.txt", content)
	if _, err := runEditTool(t, root, "f.txt", "start\n...\nend\n", "begin\n...\nfinish\n", true); err != nil {
		t.Fatalf("elision via tool: %v", err)
	}
	if content := readFixture(t, path); content != want {
		t.Fatalf("content = %q, want %q", content, want)
	}
}

func TestEditRepairElisionRejectsAmbiguousPiece(t *testing.T) {
	content := "dup\nalpha\nmid\ndup\nbeta\n"
	if got, ok := repairReplace(content, "dup\n...\nbeta\n", "one\n...\ntwo\n"); ok {
		t.Fatalf("ambiguous anchor was accepted: %q", got)
	}

	root := t.TempDir()
	writeFixture(t, root, "f.txt", content)
	_, err := runEditTool(t, root, "f.txt", "dup\n...\nbeta\n", "one\n...\ntwo\n", true)
	if err == nil {
		t.Fatal("expected a repair diagnostic for the ambiguous elision")
	}
	if !strings.Contains(err.Error(), "edit repair failed") {
		t.Fatalf("err = %q, want a repair diagnostic", err)
	}
	writeFixture(t, root, "g.txt", content)
	if got := readFixture(t, filepath.Join(root, "f.txt")); got != content {
		t.Fatalf("failed edit changed the file: %q", got)
	}
}

func TestRepairReplaceNormalizesPunctuation(t *testing.T) {
	tests := []struct {
		name, content, old, newText, want string
	}{
		{
			name:    "curly single quotes",
			content: "she said 'hello' softly\n",
			old:     "she said ‘hello’ softly",
			newText: "she said 'goodbye' softly",
			want:    "she said 'goodbye' softly\n",
		},
		{
			name:    "curly double quotes",
			content: "he said \"stop\" loud\n",
			old:     "he said “stop” loud",
			newText: "he said \"go\" loud",
			want:    "he said \"go\" loud\n",
		},
		{
			name:    "en and em dashes",
			content: "one - two - three\n",
			old:     "one – two — three",
			newText: "ONE - TWO - THREE",
			want:    "ONE - TWO - THREE\n",
		},
		{
			name:    "nbsp and ideographic space",
			content: "a b c\n",
			old:     "a\u00a0b\u3000c",
			newText: "A B C",
			want:    "A B C\n",
		},
		{
			name:    "reverse direction straight old curly file",
			content: "it’s fine\n",
			old:     "it's fine",
			newText: "it's ok",
			want:    "it's ok\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := repairReplace(tt.content, tt.old, tt.newText)
			if !ok {
				t.Fatal("punctuation-normalized match not found")
			}
			if got != tt.want {
				t.Fatalf("result = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNormalizePunctRuneExpandedMapping(t *testing.T) {
	// Per-rune substitutions introduced for the codex audit: horizontal bars
	// and the remaining Unicode space family. Every case must map to a single
	// ASCII rune and never change rune length.
	tests := []struct {
		name string
		in   rune
		want rune
	}{
		{"hyphen U+2010", '\u2010', '-'},
		{"non-breaking hyphen U+2011", '\u2011', '-'},
		{"figure dash U+2012", '\u2012', '-'},
		{"en dash U+2013", '\u2013', '-'},
		{"em dash U+2014", '\u2014', '-'},
		{"horizontal bar U+2015", '\u2015', '-'},
		{"minus sign U+2212", '\u2212', '-'},
		{"no-break space U+00A0", '\u00a0', ' '},
		{"en space U+2002", '\u2002', ' '},
		{"em space U+2003", '\u2003', ' '},
		{"three-per-em U+2004", '\u2004', ' '},
		{"four-per-em U+2005", '\u2005', ' '},
		{"six-per-em U+2006", '\u2006', ' '},
		{"figure space U+2007", '\u2007', ' '},
		{"punctuation space U+2008", '\u2008', ' '},
		{"thin space U+2009", '\u2009', ' '},
		{"hair space U+200A", '\u200a', ' '},
		{"narrow no-break U+202F", '\u202f', ' '},
		{"medium math U+205F", '\u205f', ' '},
		{"ideographic space U+3000", '\u3000', ' '},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizePunctRune(tt.in); got != tt.want {
				t.Fatalf("normalizePunctRune(%U) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestRepairReplaceNormalizesExpandedPunctuation(t *testing.T) {
	// Integration: each newly mapped rune must let a finite old block match the
	// ASCII file text through repairReplace, and leave unrelated bytes alone.
	// content is the ASCII form; old is the same text with one rune swapped.
	tests := []struct {
		name, content, old, want string
	}{
		{"hyphen U+2010", "keep\na-b c\n", "a\u2010b c", "keep\nX\n"},
		{"non-breaking hyphen U+2011", "keep\na-b c\n", "a\u2011b c", "keep\nX\n"},
		{"figure dash U+2012", "keep\na-b c\n", "a\u2012b c", "keep\nX\n"},
		{"minus sign U+2212", "keep\na-b c\n", "a\u2212b c", "keep\nX\n"},
		{"en space U+2002", "keep\na b c\n", "a\u2002b c", "keep\nX\n"},
		{"em space U+2003", "keep\na b c\n", "a\u2003b c", "keep\nX\n"},
		{"thin space U+2009", "keep\na b c\n", "a\u2009b c", "keep\nX\n"},
		{"hair space U+200A", "keep\na b c\n", "a\u200ab c", "keep\nX\n"},
		{"narrow no-break U+202F", "keep\na b c\n", "a\u202fb c", "keep\nX\n"},
		{"medium math U+205F", "keep\na b c\n", "a\u205fb c", "keep\nX\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := repairReplace(tt.content, tt.old, "X")
			if !ok {
				t.Fatalf("punctuation-normalized match not found for %q", tt.old)
			}
			if got != tt.want {
				t.Fatalf("result = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRepairReplacePunctuationRejectsAmbiguousMatch(t *testing.T) {
	// Both windows normalize to "a-b", so no single occurrence may be picked.
	content := "a-b and a-b\n"
	if got, ok := repairReplace(content, "a–b", "X"); ok {
		t.Fatalf("ambiguous normalized match was accepted: %q", got)
	}
}

func TestRepairReplacePunctuationDoesNotMatchUnrelatedText(t *testing.T) {
	// Plain text with no punctuation issues must not be fuzzy-matched.
	if got, ok := repairReplace("alpha beta gamma\n", "completely different", "X"); ok {
		t.Fatalf("unrelated text matched: %q", got)
	}
	// Only the full phrase matches after normalization: "this" occurs twice,
	// but "fix 'this' now" occurs once, so the correct window is chosen.
	content := "fix 'this' now and keep 'this' later\n"
	got, ok := repairReplace(content, "fix ‘this’ now", "FIXED")
	if !ok || got != "FIXED and keep 'this' later\n" {
		t.Fatalf("result = %q, ok = %v, want the first window only", got, ok)
	}
}

func TestEditRepairPunctuationLeavesUnrelatedBytesUntouched(t *testing.T) {
	root := t.TempDir()
	// The curly quote on the first line is unrelated and must survive verbatim.
	path := writeFixture(t, root, "f.txt", "keep ‘this’\nchange 'this'\n")

	_, err := runEditTool(t, root, "f.txt", "change ‘this’", "changed", true)
	if err != nil {
		t.Fatalf("punctuation repair via tool: %v", err)
	}
	if got, want := readFixture(t, path), "keep ‘this’\nchanged\n"; got != want {
		t.Fatalf("content = %q, want %q (unrelated curly quote preserved)", got, want)
	}
}

func TestEditRepairFalseDoesNotNormalizePunctuation(t *testing.T) {
	root := t.TempDir()
	path := writeFixture(t, root, "f.txt", "say 'hi'\n")

	_, err := runEditTool(t, root, "f.txt", "say ‘hi’", "X", false)
	if err == nil {
		t.Fatal("repair=false should not normalize punctuation")
	}
	if got := readFixture(t, path); got != "say 'hi'\n" {
		t.Fatalf("failed edit changed the file: %q", got)
	}
}

func TestRepairReplaceTrailingWhitespace(t *testing.T) {
	tests := []struct {
		name, content, old, newText, want string
	}{
		{
			name:    "file has trailing spaces old does not",
			content: "alpha \nbeta \ngamma\n",
			old:     "alpha\nbeta",
			newText: "X\nY",
			want:    "X\nY\ngamma\n",
		},
		{
			name:    "old has different trailing whitespace than file",
			content: "alpha\t\nbeta\n",
			old:     "alpha \nbeta",
			newText: "ALPHA\nBETA",
			want:    "ALPHA\nBETA\n",
		},
		{
			// old uses a tab so the exact-literal rung cannot win (it is not a
			// substring of the space-padded file line); only rstrip matches.
			name:    "single line trailing whitespace",
			content: "foo   \n",
			old:     "foo\t",
			newText: "bar",
			want:    "bar\n",
		},
		{
			name:    "CJK lines with trailing whitespace",
			content: "保留 \n你好  \n世界\n",
			old:     "你好\n世界",
			newText: "再见\n世界",
			want:    "保留 \n再见\n世界\n",
		},
		{
			name:    "interior lines preserved outside the window",
			content: "head\na \nb \ntail\n",
			old:     "a\nb",
			newText: "NEW",
			want:    "head\nNEW\ntail\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := repairReplace(tt.content, tt.old, tt.newText)
			if !ok {
				t.Fatal("trailing-whitespace repair did not match")
			}
			if got != tt.want {
				t.Fatalf("result = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRepairReplaceTrailingWhitespaceRejectsAmbiguousMatch(t *testing.T) {
	// Both "foo \nbar" and "foo\t\nbar" normalize to ["foo", "bar"], so no
	// single window may be picked.
	content := "foo \nbar\nfoo\t\nbar\n"
	if got, ok := repairReplace(content, "foo\nbar", "X"); ok {
		t.Fatalf("ambiguous trailing-whitespace match was accepted: %q", got)
	}

	root := t.TempDir()
	writeFixture(t, root, "f.txt", content)
	if _, err := runEditTool(t, root, "f.txt", "foo\nbar", "X", true); err == nil {
		t.Fatal("expected a repair diagnostic for the ambiguous trailing-whitespace match")
	}
	if got := readFixture(t, filepath.Join(root, "f.txt")); got != content {
		t.Fatalf("failed edit changed the file: %q", got)
	}
}

func TestEditRepairTrailingWhitespaceLeavesUnrelatedLinesUntouched(t *testing.T) {
	root := t.TempDir()
	// The first line's trailing spaces are unrelated to the edited block and
	// must survive byte-for-byte.
	path := writeFixture(t, root, "f.txt", "keep   \nchange \nlast\n")

	_, err := runEditTool(t, root, "f.txt", "change\nlast", "DONE\nlast", true)
	if err != nil {
		t.Fatalf("trailing-whitespace repair via tool: %v", err)
	}
	if got, want := readFixture(t, path), "keep   \nDONE\nlast\n"; got != want {
		t.Fatalf("content = %q, want %q (unrelated trailing spaces preserved)", got, want)
	}
}

func TestRepairReplaceTrailingWhitespacePreservesCRLF(t *testing.T) {
	// CRLF files: the \r must be treated as trailing whitespace for matching,
	// yet the original CRLF line endings must survive the splice.
	content := "a \r\nb\r\nc\r\n"
	got, ok := repairReplace(content, "a\nb", "X")
	if !ok {
		t.Fatal("trailing-whitespace repair did not match a CRLF file")
	}
	if want := "X\nc\r\n"; got != want {
		t.Fatalf("result = %q, want %q (untouched CRLF line preserved)", got, want)
	}
}

func TestSimilarLinesRanksMostSimilarFirst(t *testing.T) {
	lines := similarLines("alpha beta\nneedle one\nneedle two\n", "needle one", 5)
	if len(lines) == 0 || lines[0] != "needle one" {
		t.Fatalf("similarLines = %q, want needle one first", lines)
	}
	if len(lines) > 5 {
		t.Fatalf("limit ignored: got %d lines", len(lines))
	}
}

func TestEditRepairFailureReturnsSimilarLines(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "f.txt", "alpha beta\nneedle one\nneedle two\n")

	_, err := runEditTool(t, root, "f.txt", "needle wan", "X", true)
	if err == nil {
		t.Fatal("expected a repair diagnostic")
	}
	msg := err.Error()
	for _, want := range []string{
		"edit repair failed",
		"after 1 edit(s) attempted",
		"file unchanged",
		"most similar lines:",
		"needle one",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("diagnostic %q missing %q", msg, want)
		}
	}
}

func TestEditRepairFalsePreservesOriginalErrorStrings(t *testing.T) {
	root := t.TempDir()
	path := writeFixture(t, root, "x.txt", "dup\ndup\n")

	_, err := runEditTool(t, root, "x.txt", "dup", "done", false)
	want := "edit: old text appears 2 times in x.txt (not unique); a unique match is required"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want exactly %q", err, want)
	}

	_, err = runEditTool(t, root, "x.txt", "missing", "done", false)
	want = "edit: old text not found in x.txt"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want exactly %q", err, want)
	}
	if content := readFixture(t, path); content != "dup\ndup\n" {
		t.Fatalf("failed edits changed the file: %q", content)
	}
}
