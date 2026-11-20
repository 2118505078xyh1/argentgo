package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// writeAnchorFixture writes a small source file whose 7th line carries a
// credential-shaped secret. It returns the workspace root.
func writeAnchorFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	src := strings.Join([]string{
		"package sample",
		"",
		"func check() int {",
		"    a := 1",
		"    b := 2",
		"    c := 3",
		"    api_key=sk_live_abcdefghijklmnop", // offending line 7
		"    return a + b + c",
		"}",
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(root, "f.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return root
}

// outputPortion returns the bounded diagnostic section of a FAIL feedback:
// everything after the fix-instruction line and the verdict line.
func outputPortion(feedback string) (string, bool) {
	_, afterFix, found := strings.Cut(feedback, "\n")
	if !found {
		return "", false
	}
	_, rest, found := strings.Cut(afterFix, "\n")
	return rest, found
}

func TestVerificationFeedbackAtAppendsAnchoredSourceWindow(t *testing.T) {
	root := writeAnchorFixture(t)
	const secret = "sk_live_abcdefghijklmnop"
	out := "compiling\nf.go:7: api_key=" + secret + "\nsome failure detail\n"

	got := VerificationFeedbackAt("lint", false, out, 4096, root)

	if !strings.HasPrefix(got, verifyFixInstruction+"\n[verify: LINT] FAIL\n") {
		t.Fatalf("feedback = %q, want fix instruction then FAIL verdict header", got)
	}
	if !strings.Contains(got, "f.go:7:") {
		t.Fatalf("feedback = %q, want anchored file:line header", got)
	}
	if !strings.Contains(got, "█7|") {
		t.Fatalf("feedback = %q, want █-marked offending line", got)
	}
	if !strings.Contains(got, verifyAnchorExplanation+"\n") {
		t.Fatalf("feedback = %q, want the █ explanation line", got)
	}
	// The explanation belongs immediately before the source window, not before
	// the log tail (and never on a source-window-free FAIL).
	if expAt, anchorAt := strings.Index(got, verifyAnchorExplanation), strings.Index(got, "f.go:7:\n"); expAt < 0 || anchorAt < 0 || expAt > anchorAt {
		t.Fatalf("feedback = %q, want the █ explanation before the source window", got)
	}
	if !strings.Contains(got, "some failure detail") {
		t.Fatalf("feedback = %q, want the redacted log tail", got)
	}
	// The source window must follow the log tail, not precede it.
	tailAt := strings.Index(got, "some failure detail")
	anchorAt := strings.Index(got, "█7|")
	if tailAt < 0 || anchorAt < 0 || tailAt > anchorAt {
		t.Fatalf("feedback = %q, want source window appended after the tail", got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("feedback is not valid UTF-8: %q", got)
	}
	// Redaction covers both the echoed output and the source line itself.
	if strings.Contains(got, secret) {
		t.Fatalf("feedback leaked the secret: %q", got)
	}
	if !strings.Contains(got, "[redacted]") {
		t.Fatalf("feedback = %q, want a [redacted] marker", got)
	}
}

func TestVerificationFeedbackAtPassIsUnchanged(t *testing.T) {
	root := writeAnchorFixture(t)
	const out = "f.go:7 noisy pass output"
	got := VerificationFeedbackAt("lint", true, out, 4096, root)
	want := VerificationFeedback("lint", true, out, 4096)
	if got != want {
		t.Fatalf("PASS feedback = %q, want unchanged %q", got, want)
	}
	if strings.Contains(got, "█") {
		t.Fatalf("PASS feedback must not contain a source window: %q", got)
	}
}

func TestVerificationFeedbackAtNoAnchorAddsFixInstructionOnly(t *testing.T) {
	root := writeAnchorFixture(t)
	out := "totally plain failure, no file reference here"
	got := VerificationFeedbackAt("test", false, out, 4096, root)
	want := verifyFixInstruction + "\n[verify: TEST] FAIL\n" + out
	if got != want {
		t.Fatalf("no-anchor feedback = %q, want %q", got, want)
	}
	if strings.Contains(got, verifyAnchorExplanation) || strings.Contains(got, "█") {
		t.Fatalf("no-anchor feedback gained a █ explanation: %q", got)
	}
}

func TestVerificationFeedbackAtEmptyRootAddsFixInstructionOnly(t *testing.T) {
	root := writeAnchorFixture(t)
	out := "f.go:7: failure detail"
	want := verifyFixInstruction + "\n[verify: TEST] FAIL\n" + out
	for _, emptyRoot := range []string{"", "   \t "} {
		if got := VerificationFeedbackAt("test", false, out, 4096, emptyRoot); got != want {
			t.Fatalf("empty-root %q feedback = %q, want %q", emptyRoot, got, want)
		}
	}
	// Sanity: a non-empty root does anchor, proving the fixture is usable.
	if got := VerificationFeedbackAt("test", false, out, 4096, root); !strings.Contains(got, "█") {
		t.Fatalf("non-empty root did not anchor the valid fixture: %q", got)
	}
}

func TestVerificationFeedbackAtOutOfRootAnchorAddsFixInstructionOnly(t *testing.T) {
	writeAnchorFixture(t)
	out := "../outside.go:3: failure detail"
	root := t.TempDir()
	got := VerificationFeedbackAt("test", false, out, 4096, root)
	want := verifyFixInstruction + "\n[verify: TEST] FAIL\n" + out
	if got != want {
		t.Fatalf("out-of-root feedback = %q, want %q", got, want)
	}
	if strings.Contains(got, verifyAnchorExplanation) {
		t.Fatalf("out-of-root feedback gained a █ explanation: %q", got)
	}
}

func TestVerificationFeedbackStaysUnchangedWhileAtAddsFixInstruction(t *testing.T) {
	out := "f.go:7: failure\napi_key=sk_live_abcdefghijklmnop"
	for _, maxBytes := range []int{0, 64, 4096} {
		plain := VerificationFeedback("lint", false, out, maxBytes)
		at := VerificationFeedbackAt("lint", false, out, maxBytes, "")
		if want := verifyFixInstruction + "\n" + plain; at != want {
			t.Fatalf("maxBytes=%d: VerificationFeedbackAt = %q, want %q", maxBytes, at, want)
		}
	}
}

func TestVerificationFeedbackAtRespectsMaxBytes(t *testing.T) {
	root := writeAnchorFixture(t)
	out := strings.Repeat("q", 400) + "\nf.go:7: api_key=sk_live_abcdefghijklmnop\n" + strings.Repeat("z", 400)
	const maxBytes = 200

	got := VerificationFeedbackAt("test", false, out, maxBytes, root)
	portion, found := outputPortion(got)
	if !found {
		t.Fatalf("feedback has no output section: %q", got)
	}
	if len(portion) > maxBytes {
		t.Fatalf("output portion = %d bytes, want <= %d: %q", len(portion), maxBytes, got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("feedback is not valid UTF-8: %q", got)
	}
	if !strings.Contains(got, "█7|") {
		t.Fatalf("feedback = %q, want the █-marked window to survive the bound", got)
	}
	if strings.Contains(got, "sk_live_abcdefghijklmnop") {
		t.Fatalf("feedback leaked the secret: %q", got)
	}
}
