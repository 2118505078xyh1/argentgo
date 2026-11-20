package session

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestFailureReasonRecognizesExitMarkerInAnyCase covers a real compaction bug:
// failureReason located "[exit N]" case-insensitively but then stripped the
// literal lowercase prefix from the ORIGINAL text. A shell that printed
// "[EXIT 1]" (or "[Exit 2]") therefore parsed no digits and the failure was
// dropped from the compaction summary even though the marker had been found.
// The digits must be read from the lowered string, where case folding cannot
// change ASCII digits, while the returned text keeps the original spelling.
func TestFailureReasonRecognizesExitMarkerInAnyCase(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{"lowercase", "compile done [exit 1]", "[exit 1]"},
		{"uppercase", "compile done [EXIT 1]", "[EXIT 1]"},
		{"mixed case", "compile done [Exit 2]", "[Exit 2]"},
		{"uppercase after unicode", "编译完成 [EXIT 3]", "[EXIT 3]"},
		{"unicode before marker", "K done [EXIT 4]", "[EXIT 4]"},
		{"dotted capital i before marker", "İ done [EXIT 5]", "[EXIT 5]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason, ok := failureReason(tc.content)
			if !ok {
				t.Fatalf("failureReason(%q) found no failure", tc.content)
			}
			if reason != tc.want {
				t.Errorf("failureReason(%q) = %q, want %q", tc.content, reason, tc.want)
			}
			if !utf8.ValidString(reason) {
				t.Errorf("failureReason(%q) returned invalid UTF-8 %q", tc.content, reason)
			}
		})
	}
}

// TestFailureReasonStillRejectsSuccessfulAndMalformedExitMarkers guards the
// negative half of the fix: only a non-zero numeric exit code is a failure,
// regardless of the marker's case.
func TestFailureReasonStillRejectsSuccessfulAndMalformedExitMarkers(t *testing.T) {
	for _, content := range []string{
		"compile done [EXIT 0]",  // success
		"compile done [Exit 00]", // zero with leading zeroes
		"compile done [EXIT x]",  // non-numeric
		"compile done [EXIT -1]", // negative
	} {
		if reason, ok := failureReason(content); ok {
			t.Errorf("failureReason(%q) = %q, true; want no failure", content, reason)
		}
	}
}

// TestSummarizeKeepsUppercaseExitFailure is the end-to-end guard: a dropped
// tool result whose output carries an upper-case exit marker must still surface
// as a failure in the compaction summary, so a resumed turn does not forget it.
func TestSummarizeKeepsUppercaseExitFailure(t *testing.T) {
	dropped := []Entry{
		{Role: "tool", Name: "bash", Content: "[EXIT 7]"},
	}
	compaction := Summarize(dropped, 0)
	if !utf8.ValidString(compaction.Summary) {
		t.Fatalf("summary is not valid UTF-8: %q", compaction.Summary)
	}
	if !strings.Contains(compaction.Summary, "[EXIT 7]") {
		t.Fatalf("summary lost the upper-case exit failure: %q", compaction.Summary)
	}
	want := "bash — [EXIT 7]"
	if len(compaction.Failures) != 1 || compaction.Failures[0] != want {
		t.Fatalf("Failures = %q, want [%q]", compaction.Failures, want)
	}
}
