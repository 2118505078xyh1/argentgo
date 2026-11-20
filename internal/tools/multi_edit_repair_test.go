package tools

import (
	"strings"
	"testing"
)

func runMultiEditTool(t *testing.T, root, rel string, edits []map[string]any, repair bool) (string, error) {
	t.Helper()
	args := map[string]any{
		"path":   rel,
		"edits":  edits,
		"repair": repair,
	}
	return runToolArgs(t, root, "multi_edit", args)
}

func TestMultiEditRepairAppliesExactAndFuzzyEdits(t *testing.T) {
	root := t.TempDir()
	path := writeFixture(t, root, "f.txt", "\tfoo()\n\tbar()\nkeep\n")

	edits := []map[string]any{
		{"old": "    foo()\n    bar()", "new": "    foo()\n    BAZ()"}, // indent repair
		{"old": "keep", "new": "KEEP"},                                 // exact
	}
	out, err := runMultiEditTool(t, root, "f.txt", edits, true)
	if err != nil {
		t.Fatalf("multi_edit repair=true: %v", err)
	}
	if want := "edited f.txt (2 edits)"; out != want {
		t.Fatalf("out = %q, want %q", out, want)
	}
	if got, want := readFixture(t, path), "\tfoo()\n\tBAZ()\nKEEP\n"; got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestMultiEditRepairFailureIsAtomicAndConsistent(t *testing.T) {
	root := t.TempDir()
	content := "alpha\nbeta\ngamma\n"
	path := writeFixture(t, root, "f.txt", content)

	edits := []map[string]any{
		{"old": "alpha", "new": "ALPHA"},           // would succeed
		{"old": "no such unique text", "new": "X"}, // cannot be matched
		{"old": "gamma", "new": "GAMMA"},           // never attempted
	}
	out, err := runMultiEditTool(t, root, "f.txt", edits, true)
	if out != "" {
		t.Fatalf("out = %q, want empty on failure", out)
	}
	if err == nil {
		t.Fatal("expected a repair diagnostic for the failing batch")
	}
	msg := err.Error()
	for _, want := range []string{
		"multi_edit: repair failed at edit 2 of 3",
		"after 3 edit(s) attempted",
		"file unchanged (atomic batch)",
		"1 edit(s) matched cleanly in memory",
		"no bytes were written (atomic batch)",
		"re-send the whole batch",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("diagnostic %q missing %q", msg, want)
		}
	}
	// The instruction must not contradict the atomic write semantics: nothing
	// was written, so no edit may be described as already applied.
	for _, bad := range []string{
		"were applied successfully",
		"do not re-send them",
	} {
		if strings.Contains(msg, bad) {
			t.Fatalf("atomic diagnostic %q contradicts itself with %q", msg, bad)
		}
	}
	if got := readFixture(t, path); got != content {
		t.Fatalf("failed batch was partially written: %q", got)
	}
}

func TestMultiEditRepairErrorMessageNeverTellsModelToSkipEdits(t *testing.T) {
	// With any positive applied count the message must stay internally
	// consistent: edits matched in memory, no bytes written, re-send all.
	msg := multiEditRepairError("f.txt", 2, 3, 1, "alpha\nbeta\n", "beta").Error()
	if !strings.Contains(msg, "no bytes were written (atomic batch)") ||
		!strings.Contains(msg, "re-send the whole batch") {
		t.Fatalf("message lost its atomic guidance: %q", msg)
	}
	if strings.Contains(msg, "do not re-send") {
		t.Fatalf("message still contradicts atomic semantics: %q", msg)
	}

	// The zero-applied branch must give the same actionable guidance and must
	// not mention skipped edits.
	msg = multiEditRepairError("f.txt", 1, 1, 0, "alpha\n", "missing").Error()
	if !strings.Contains(msg, "no edit matched") || !strings.Contains(msg, "re-send the whole batch") {
		t.Fatalf("zero-applied message lost its guidance: %q", msg)
	}
	if strings.Contains(msg, "do not re-send") || strings.Contains(msg, "were applied") {
		t.Fatalf("zero-applied message claims edits were applied: %q", msg)
	}
}

func TestMultiEditRepairFalsePreservesOriginalErrorStrings(t *testing.T) {
	root := t.TempDir()
	path := writeFixture(t, root, "x.txt", "dup\ndup\n")

	_, err := runMultiEditTool(t, root, "x.txt", []map[string]any{
		{"old": "dup", "new": "done"},
	}, false)
	want := "multi_edit: edit 1: old text appears 2 times in x.txt (not unique); a unique match is required"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want exactly %q", err, want)
	}

	_, err = runMultiEditTool(t, root, "x.txt", []map[string]any{
		{"old": "missing", "new": "done"},
	}, false)
	want = "multi_edit: edit 1: old text not found in x.txt"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want exactly %q", err, want)
	}
	if content := readFixture(t, path); content != "dup\ndup\n" {
		t.Fatalf("failed edits changed the file: %q", content)
	}
}
