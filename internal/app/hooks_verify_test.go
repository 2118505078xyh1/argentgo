package app

import (
	"strings"
	"testing"
)

// TestAppendVerifyNoteAppendsPassVerdict checks that a passing verify note is
// attached as an ordinary PostToolUse-style hook line.
func TestAppendVerifyNoteAppendsPassVerdict(t *testing.T) {
	got := AppendVerifyNote("tool out", " lint ", true, "noisy output")
	want := "tool out\n[hook] [verify: LINT] PASS"
	if got != want {
		t.Fatalf("AppendVerifyNote = %q, want %q", got, want)
	}
}

// TestAppendVerifyNoteEmptyOutputIsVerdictOnly checks that a note on empty
// output is just the verdict, with no empty output section.
func TestAppendVerifyNoteEmptyOutputIsVerdictOnly(t *testing.T) {
	got := AppendVerifyNote("", "test", false, "  \n\t ")
	want := "[hook] [verify: TEST] FAIL"
	if got != want {
		t.Fatalf("AppendVerifyNote = %q, want %q", got, want)
	}
}

// TestAppendVerifyNoteFailRedactsAndKeepsTail checks that a failed verify note
// carries the redacted tail of the command output and leaves the tool's own
// result untouched.
func TestAppendVerifyNoteFailRedactsAndKeepsTail(t *testing.T) {
	cmdOutput := "api_key=sk_live_abcdefghijklmnop failed at step 3"
	got := AppendVerifyNote("edit result", "test", false, cmdOutput)

	if !strings.HasPrefix(got, "edit result\n[hook] [verify: TEST] FAIL\n") {
		t.Fatalf("note = %q, want the verdict after the tool output", got)
	}
	if strings.Contains(got, "sk_live_abcdefghijklmnop") {
		t.Fatalf("note leaked the secret: %q", got)
	}
	if !strings.Contains(got, "[redacted]") || !strings.HasSuffix(got, "failed at step 3") {
		t.Fatalf("note = %q, want a redacted output tail", got)
	}
}
