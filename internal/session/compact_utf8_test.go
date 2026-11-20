package session

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestFailureReasonKeepsExitCodeAfterLowercaseLengthChange covers a real
// compaction bug: failureReason lowercased the whole tool output to locate the
// "[exit N]" marker case-insensitively, then used that LOWERCASED byte offset to
// slice the ORIGINAL text. strings.ToLower folds rune-by-rune, so a rune whose
// lowercase form is fewer bytes (İ 2→1, ẞ 3→2, K 3→1) before the marker shifts
// the offset off a rune boundary. The result was a garbled tail, a failed prefix
// match, and — when no other failure marker existed — the failure being dropped
// from the compaction summary entirely. The offset must be mapped back through
// rune indices before slicing the original string.
func TestFailureReasonKeepsExitCodeAfterLowercaseLengthChange(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"ascii", "compile done [exit 1]"},
		{"dotted-capital-i", "İ done [exit 1]"}, // U+0130 -> i: 2 bytes -> 1
		{"capital-sharp-s", "ẞ done [exit 1]"},  // U+1E9E -> ß: 3 bytes -> 2
		{"kelvin-sign", "K done [exit 1]"},      // U+212A -> k: 3 bytes -> 1
		{"cjk-unchanged", "编译完成 [exit 1]"},      // CJK lowercases to itself
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason, ok := failureReason(tc.content)
			if !ok {
				t.Fatalf("failureReason(%q) found no failure", tc.content)
			}
			if reason != "[exit 1]" {
				t.Errorf("failureReason(%q) = %q, want %q", tc.content, reason, "[exit 1]")
			}
			if !utf8.ValidString(reason) {
				t.Errorf("failureReason(%q) returned invalid UTF-8 %q", tc.content, reason)
			}
		})
	}
}

// TestSummarizeKeepsFailureWithUnicodeBeforeExitCode is the end-to-end guard:
// a dropped tool result whose output carries a length-changing rune before an
// exit code must still surface as a failure in the compaction summary, rather
// than silently losing the evidence a resumed turn needs.
func TestSummarizeKeepsFailureWithUnicodeBeforeExitCode(t *testing.T) {
	dropped := []Entry{
		{Role: "tool", Name: "bash", Content: "K done [exit 2]"},
	}
	compaction := Summarize(dropped, 0)
	if !utf8.ValidString(compaction.Summary) {
		t.Fatalf("summary is not valid UTF-8: %q", compaction.Summary)
	}
	if !strings.Contains(compaction.Summary, "[exit 2]") {
		t.Fatalf("summary lost the exit-code failure: %q", compaction.Summary)
	}
	if len(compaction.Failures) != 1 || compaction.Failures[0] != "bash — [exit 2]" {
		t.Fatalf("Failures = %q, want [\"bash — [exit 2]\"]", compaction.Failures)
	}
}
