package agent

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestVerificationFeedbackPassIsVerdictOnly(t *testing.T) {
	got := VerificationFeedback(" lint ", true, "noisy output that should be dropped", 4096)
	want := "[verify: LINT] PASS"
	if got != want {
		t.Fatalf("VerificationFeedback(pass) = %q, want %q", got, want)
	}
}

func TestVerificationFeedbackFailKeepsTail(t *testing.T) {
	got := VerificationFeedback("test", false, "step 1 ok\nstep 2 ok\nfinal failure: assertion x", 4096)
	wantPrefix := "[verify: TEST] FAIL\n"
	if !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("feedback = %q, want prefix %q", got, wantPrefix)
	}
	if !strings.HasSuffix(got, "final failure: assertion x") {
		t.Fatalf("feedback = %q, want the last line kept", got)
	}
}

func TestVerificationFeedbackTailIsBoundedAndUTF8Safe(t *testing.T) {
	const maxBytes = 32
	out := strings.Repeat("q", 200) + "界界界" + "FAIL_TAIL" // cut lands mid-rune
	got := VerificationFeedback("test", false, out, maxBytes)

	if !utf8.ValidString(got) {
		t.Fatalf("feedback is not valid UTF-8: %q", got)
	}
	_, clipped, found := strings.Cut(got, "\n")
	if !found {
		t.Fatalf("feedback has no output section: %q", got)
	}
	if len(clipped) > maxBytes {
		t.Fatalf("output section = %d bytes, want <= %d", len(clipped), maxBytes)
	}
	if !strings.HasPrefix(clipped, verifyTruncatedMarker) {
		t.Fatalf("output = %q, want truncation marker", clipped)
	}
	if !strings.Contains(clipped, "界") || !strings.Contains(clipped, "FAIL_TAIL") {
		t.Fatalf("output = %q, want the tail runes and FAIL_TAIL kept", clipped)
	}
	if strings.Contains(clipped, "qqqqqqqqqq") {
		t.Fatalf("output = %q, want the leading bytes dropped", clipped)
	}
}

func TestVerificationFeedbackRedactsFakeSecret(t *testing.T) {
	out := "api_key=sk_live_abcdefghijklmnop failed at step 3"
	got := VerificationFeedback("test", false, out, 4096)
	if strings.Contains(got, "sk_live_abcdefghijklmnop") {
		t.Fatalf("feedback leaked the secret: %q", got)
	}
	if !strings.Contains(got, "[redacted]") {
		t.Fatalf("feedback = %q, want a [redacted] marker", got)
	}
}

func TestVerificationFeedbackEmptyOutputIsVerdictOnly(t *testing.T) {
	got := VerificationFeedback("lint", false, "  \n\t ", 4096)
	if want := "[verify: LINT] FAIL"; got != want {
		t.Fatalf("feedback = %q, want %q", got, want)
	}
}

func TestVerificationFeedbackUsesDefaultMaxBytesForZero(t *testing.T) {
	out := strings.Repeat("Z", defaultVerifyMaxBytes+200)
	got := VerificationFeedback("test", false, out, 0)
	_, clipped, found := strings.Cut(got, "\n")
	if !found {
		t.Fatalf("feedback has no output section: %q", got)
	}
	if len(clipped) > defaultVerifyMaxBytes {
		t.Fatalf("output section = %d bytes, want <= default %d", len(clipped), defaultVerifyMaxBytes)
	}
	if !strings.HasPrefix(clipped, verifyTruncatedMarker) {
		t.Fatalf("output = %q, want truncation marker", clipped)
	}
}

func TestVerificationFeedbackBlankLabelFallsBack(t *testing.T) {
	if got, want := VerificationFeedback("   ", true, "", 0), "[verify: VERIFY] PASS"; got != want {
		t.Fatalf("feedback = %q, want %q", got, want)
	}
}

func TestFormatVerifyResultsBatchesResults(t *testing.T) {
	results := []VerifyResult{
		{Label: "lint", Passed: true, Output: "ignored"},
		{Label: "test", Passed: false, Output: "boom"},
	}
	got := FormatVerifyResults(results, 4096)
	for _, want := range []string{"[verify: LINT] PASS", "[verify: TEST] FAIL", "boom"} {
		if !strings.Contains(got, want) {
			t.Errorf("batch = %q, want it to contain %q", got, want)
		}
	}
}

func TestFormatVerifyResultsKeepsOnlyTailWhenBounded(t *testing.T) {
	results := []VerifyResult{
		{Label: "a", Passed: false, Output: strings.Repeat("q", 5000)},
		{Label: "b", Passed: false, Output: "FINAL_BAD"},
	}
	got := FormatVerifyResults(results, 128)
	if !utf8.ValidString(got) {
		t.Fatalf("batch is not valid UTF-8: %q", got)
	}
	if !strings.Contains(got, "FINAL_BAD") {
		t.Fatalf("batch = %q, want the last failure kept", got)
	}
	if strings.Contains(got, "[verify: A] FAIL") {
		t.Fatalf("batch = %q, want the earlier failure clipped away", got)
	}
}

func TestFormatVerifyResultsEmptyIsEmpty(t *testing.T) {
	if got := FormatVerifyResults(nil, 0); got != "" {
		t.Fatalf("FormatVerifyResults(nil) = %q, want empty", got)
	}
}
