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

func outputPortion(feedback string) (string, bool) {
	_, rest, found := strings.Cut(feedback, "\n")
	return rest, found
}

func TestVerificationFeedbackAtAppendsAnchoredSourceWindow(t *testing.T) {
	root := writeAnchorFixture(t)
	const secret = "sk_live_abcdefghijklmnop"
	out := "compiling\nf.go:7: api_key=" + secret + "\nsome failure detail\n"

	got := VerificationFeedbackAt("lint", false, out, 4096, root)

	if !strings.HasPrefix(got, "[verify: LINT] FAIL\n") {
		t.Fatalf("feedback = %q, want FAIL verdict header", got)
	}
	if !strings.Contains(got, "f.go:7:") {
		t.Fatalf("feedback = %q, want anchored file:line header", got)
	}
	if !strings.Contains(got, "█7|") {
		t.Fatalf("feedback = %q, want █-marked offending line", got)
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

func TestVerificationFeedbackAtNoAnchorIsUnchanged(t *testing.T) {
	root := writeAnchorFixture(t)
	out := "totally plain failure, no file reference here"
	got := VerificationFeedbackAt("test", false, out, 4096, root)
	want := VerificationFeedback("test", false, out, 4096)
	if got != want {
		t.Fatalf("no-anchor feedback = %q, want unchanged %q", got, want)
	}
}

func TestVerificationFeedbackAtEmptyRootIsUnchanged(t *testing.T) {
	root := writeAnchorFixture(t)
	out := "f.go:7: failure detail"
	want := VerificationFeedback("test", false, out, 4096)
	if got := VerificationFeedbackAt("test", false, out, 4096, ""); got != want {
		t.Fatalf("empty-root feedback = %q, want unchanged %q", got, want)
	}
	// A blank root must behave the same as an empty one.
	if got := VerificationFeedbackAt("test", false, out, 4096, "   \t "); got != want {
		t.Fatalf("blank-root feedback = %q, want unchanged %q", got, want)
	}
	// Sanity: a non-empty root does anchor, proving the fixture is usable.
	if got := VerificationFeedbackAt("test", false, out, 4096, root); got == want {
		t.Fatalf("non-empty root did not anchor the valid fixture: %q", got)
	}
}

func TestVerificationFeedbackAtOutOfRootAnchorIsUnchanged(t *testing.T) {
	writeAnchorFixture(t)
	out := "../outside.go:3: failure detail"
	root := t.TempDir()
	got := VerificationFeedbackAt("test", false, out, 4096, root)
	if want := VerificationFeedback("test", false, out, 4096); got != want {
		t.Fatalf("out-of-root feedback = %q, want unchanged %q", got, want)
	}
}

func TestVerificationFeedbackDelegatesWithEmptyRoot(t *testing.T) {
	out := "f.go:7: failure\napi_key=sk_live_abcdefghijklmnop"
	for _, maxBytes := range []int{0, 64, 4096} {
		got := VerificationFeedback("lint", false, out, maxBytes)
		want := VerificationFeedbackAt("lint", false, out, maxBytes, "")
		if got != want {
			t.Fatalf("maxBytes=%d: VerificationFeedback = %q, want %q", maxBytes, got, want)
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
