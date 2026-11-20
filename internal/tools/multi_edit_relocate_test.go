package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// runMultiEditRelocate invokes multi_edit with an explicit relocate flag so the
// opt-in path can be tested alongside the untouched default path.
func runMultiEditRelocate(t *testing.T, root, rel string, edits []map[string]any, repair, relocate bool) (string, error) {
	t.Helper()
	return runToolArgs(t, root, "multi_edit", map[string]any{
		"path":     rel,
		"edits":    edits,
		"repair":   repair,
		"relocate": relocate,
	})
}

// With relocate absent (the default), a mistyped filename must produce exactly
// the legacy error string: no candidate hint, no extra text, no behaviour
// change at all. This guards the opt-in promise byte-for-byte.
func TestMultiEditRelocateDefaultOffIsByteIdentical(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "wrong.txt", "nothing here\n")
	writeFixture(t, root, "right.txt", "apple\nbanana\n")

	_, err := runToolArgs(t, root, "multi_edit", map[string]any{
		"path":  "wrong.txt",
		"edits": []map[string]any{{"old": "banana", "new": "BANANA"}},
	})
	want := "multi_edit: edit 1: old text not found in wrong.txt"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want exactly %q", err, want)
	}
	if strings.Contains(err.Error(), "right.txt") || strings.Contains(err.Error(), "possible filename typo") {
		t.Fatalf("default-off error leaked a relocation hint: %q", err)
	}

	// The repair=true default path must stay free of hints as well.
	_, err = runToolArgs(t, root, "multi_edit", map[string]any{
		"path":   "wrong.txt",
		"repair": true,
		"edits":  []map[string]any{{"old": "banana", "new": "BANANA"}},
	})
	if err == nil {
		t.Fatal("expected a repair diagnostic")
	}
	if strings.Contains(err.Error(), "right.txt") || strings.Contains(err.Error(), "possible filename typo") {
		t.Fatalf("repair default-off error leaked a relocation hint: %q", err)
	}
}

// Opt-in relocation reports where the SEARCH text actually lives, but never
// writes to that file: the model must correct the filename and re-send.
func TestMultiEditRelocateReportsCandidateWithoutWriting(t *testing.T) {
	root := t.TempDir()
	wrong := writeFixture(t, root, "wrong.txt", "nothing here\n")
	right := writeFixture(t, root, "right.txt", "apple\nbanana\n")

	out, err := runMultiEditRelocate(t, root, "wrong.txt", []map[string]any{
		{"old": "banana", "new": "BANANA"},
	}, false, true)
	if out != "" {
		t.Fatalf("out = %q, want empty on failure", out)
	}
	if err == nil {
		t.Fatal("expected a diagnostic for the mistyped filename")
	}
	msg := err.Error()
	for _, want := range []string{
		"old text not found in wrong.txt",
		"possible filename typo",
		"right.txt",
		"none were modified; correct the path and re-send",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("diagnostic %q missing %q", msg, want)
		}
	}
	if got := readFixture(t, right); got != "apple\nbanana\n" {
		t.Fatalf("candidate file was modified: %q", got)
	}
	if got := readFixture(t, wrong); got != "nothing here\n" {
		t.Fatalf("intended file was modified: %q", got)
	}
}

// The relocation hint also works when the only recoverable match is a fuzzy
// one (repair ladder), and it is appended to the repair diagnostic rather than
// replacing it.
func TestMultiEditRelocateReportsFuzzyCandidateInRepairMode(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "wrong.txt", "nothing here\n")
	writeFixture(t, root, "right.txt", "\tfoo()\n\tbar()\n")

	_, err := runMultiEditRelocate(t, root, "wrong.txt", []map[string]any{
		{"old": "    foo()\n    bar()", "new": "    foo()\n    BAZ()"},
	}, true, true)
	if err == nil {
		t.Fatal("expected a diagnostic for the mistyped filename")
	}
	msg := err.Error()
	for _, want := range []string{
		"multi_edit: repair failed at edit 1 of 1",
		"re-send the whole batch",
		"possible filename typo",
		"right.txt (fuzzy match)",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("diagnostic %q missing %q", msg, want)
		}
	}
	if got := readFixture(t, filepath.Join(root, "right.txt")); got != "\tfoo()\n\tbar()\n" {
		t.Fatalf("candidate file was modified: %q", got)
	}
}

// Candidate paths with CJK characters must survive the walk, the canonical
// path comparison and the relative-path formatting as valid UTF-8.
func TestMultiEditRelocateReportsCJKCandidate(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "文档")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	right := filepath.Join(dir, "正确.txt")
	if err := os.WriteFile(right, []byte("苹果\n香蕉\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, root, "错误.txt", "空\n")

	_, err := runMultiEditRelocate(t, root, "错误.txt", []map[string]any{
		{"old": "香蕉", "new": "香瓜"},
	}, false, true)
	if err == nil {
		t.Fatal("expected a diagnostic for the mistyped CJK filename")
	}
	msg := err.Error()
	for _, want := range []string{
		"old text not found in 错误.txt",
		"文档/正确.txt",
		"none were modified",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("diagnostic %q missing %q", msg, want)
		}
	}
	if !utf8.ValidString(msg) || strings.ContainsRune(msg, utf8.RuneError) {
		t.Fatalf("diagnostic is not valid UTF-8: %q", msg)
	}
	if got := readFixture(t, right); got != "苹果\n香蕉\n" {
		t.Fatalf("CJK candidate file was modified: %q", got)
	}

	// Default behaviour on CJK paths stays unchanged even with the opt-in flag
	// wired in: a correct filename still edits normally.
	out, err := runMultiEditRelocate(t, root, "文档/正确.txt", []map[string]any{
		{"old": "香蕉", "new": "香瓜"},
	}, false, true)
	if err != nil {
		t.Fatalf("relocate=true with a correct CJK path: %v", err)
	}
	if want := "edited 文档/正确.txt (1 edits)"; out != want {
		t.Fatalf("out = %q, want %q", out, want)
	}
	if got := readFixture(t, right); got != "苹果\n香瓜\n" {
		t.Fatalf("content = %q", got)
	}
}
