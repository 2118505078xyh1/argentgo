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
