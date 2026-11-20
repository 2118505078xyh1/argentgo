package agent

import (
	"strings"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

const (
	// defaultVerifyMaxBytes bounds the output portion of one feedback message.
	defaultVerifyMaxBytes = 4096
	verifyTruncatedMarker = "... (truncated)"

	// verifyAnchorPad is the number of context lines AnchorSourceLines emits on
	// either side of an offending file:line reference. Two lines give the model
	// enough context to act without padding the bounded feedback with noise.
	verifyAnchorPad = 2
	// verifyAnchorSep separates the redacted log tail from the source windows.
	verifyAnchorSep = "\n"
)

// VerificationFeedback renders a compact, redacted, bounded feedback string
// suitable for injection as the next user/tool message after a verify command.
//
// A PASS is just the verdict line: the caller rarely needs the raw output of a
// successful check. A FAIL appends only the tail of output, because verify
// tools print the actionable error last. Output is redacted with
// llm.RedactText before it is clipped, and clipping never splits a UTF-8 rune.
// maxBytes bounds the output portion (the verdict line is always kept); a
// non-positive value selects defaultVerifyMaxBytes. The function is pure: no
// I/O, no shared state.
//
// This is a building block. Wire it in the layer that owns verification (for
// example after a workflow StepCheck, or in the dispatch path after a verify
// tool call) by appending the returned string to the next model message.
//
// VerificationFeedback deliberately does NOT anchor source lines: it delegates
// to VerificationFeedbackAt with an empty root, keeping its output byte-for-byte
// identical to the pre-anchoring behaviour. Callers that know the workspace
// root (the app's verify callback) should call VerificationFeedbackAt directly
// so FAIL output also shows █-marked source windows.
func VerificationFeedback(label string, passed bool, output string, maxBytes int) string {
	return VerificationFeedbackAt(label, passed, output, maxBytes, "")
}

// VerificationFeedbackAt renders the same bounded, redacted feedback as
// VerificationFeedback, and — only for FAIL output when root is non-empty and
// the output contains usable file:line references — appends the █-marked source
// windows produced by AnchorSourceLines AFTER the redacted log tail. The whole
// output portion (tail + separator + anchor windows) stays within maxBytes: a
// share of the budget is reserved for the anchor so it can never crowd out or
// exceed the bound.
//
// Backward compatibility is exact: when passed is true, when root is empty, or
// when no usable anchor is found (including an unreadable/out-of-root file), the
// returned string is identical to VerificationFeedback's output. This makes the
// function a drop-in for callers that already know the workspace root.
//
// App-side wiring (owned by internal/app, not this file): NewVerifyFunc in
// internal/app/verify_wire.go already captures `root` (it is used as
// command.Dir). In the Verify callback returned by NewVerifyFunc, replace the
// plain feedback composition with the anchored one, e.g.
//
//	fb := agent.VerificationFeedbackAt(label, passed, output, maxBytes, root)
//
// and pass `root` through the verify loop (Options -> verifyAfterBatch ->
// BuildVerifyFeedback -> VerificationFeedbackAt) so the model sees the source
// windows instead of only the log tail.
func VerificationFeedbackAt(label string, passed bool, output string, maxBytes int, root string) string {
	label = strings.ToUpper(strings.TrimSpace(label))
	if label == "" {
		label = "VERIFY"
	}
	verdict := "PASS"
	if !passed {
		verdict = "FAIL"
	}
	head := "[verify: " + label + "] " + verdict
	if passed {
		return head
	}
	clean := llm.RedactText(strings.TrimSpace(output))
	if clean == "" {
		return head
	}
	budget := resolveVerifyMaxBytes(maxBytes)
	plain := head + "\n" + clipVerifyTail(clean, budget)

	// An empty root (the legacy default) or output with no usable anchor leaves
	// the feedback byte-identical to the pre-anchoring format.
	if strings.TrimSpace(root) == "" {
		return plain
	}
	anchor := AnchorSourceLines(clean, root, verifyAnchorPad)
	if anchor == "" {
		return plain
	}

	// Reserve at most half the budget for source windows so the log tail keeps
	// room, then give the remainder (minus the separator) back to the tail.
	anchorRoom := budget / 2
	if anchorRoom <= 0 {
		return plain
	}
	anchor = clipTailBytes(anchor, anchorRoom)
	if anchor == "" {
		return plain
	}
	tailRoom := budget - len(verifyAnchorSep) - len(anchor)
	if tailRoom < 0 {
		return plain
	}
	return head + "\n" + clipVerifyTail(clean, tailRoom) + verifyAnchorSep + anchor
}

// HasDiagnosticContent reports whether a verification feedback string carries
// any substantive diagnostic information beyond the boilerplate that is always
// present: the "[verify: X] PASS/FAIL" verdict header, the reflection hint,
// redaction placeholders ([redacted]/[REDACTED]/[email]) and blank lines.
//
// It is pure and deliberately conservative: any surviving non-boilerplate
// line -- an error message, a file:line reference, CJK prose, a █-anchored
// source window -- makes it return true. A caller can therefore use a false
// result to detect the aider-P2 case: a FAIL that spent a reflection turn on a
// feedback message the model cannot act on (header + hint only).
//
// It works both on a raw verify output and on the full string returned by
// BuildVerifyFeedback/VerificationFeedback, because it strips the known
// boilerplate wherever it appears rather than assuming a fixed layout.
func HasDiagnosticContent(feedback string) bool {
	for _, line := range strings.Split(feedback, "\n") {
		text := strings.TrimSpace(line)
		if text == "" || isVerifyVerdictLine(text) || isVerifyReflectionHintLine(text) {
			continue
		}
		if isOnlyRedactionPlaceholders(text) {
			continue
		}
		return true
	}
	return false
}

// isVerifyVerdictLine recognises the "[verify: LABEL] PASS|FAIL" header line.
func isVerifyVerdictLine(line string) bool {
	if !strings.HasPrefix(line, "[verify:") {
		return false
	}
	_, rest, ok := strings.Cut(line, "]")
	if !ok {
		return false
	}
	verdict := strings.TrimSpace(rest)
	return verdict == "PASS" || verdict == "FAIL"
}

// isVerifyReflectionHintLine recognises the anti-retry line BuildVerifyFeedback
// appends to every FAIL. It is guidance, not diagnostic content, so it must not
// by itself make HasDiagnosticContent return true.
func isVerifyReflectionHintLine(line string) bool {
	return strings.HasPrefix(line, verifyReflectionHintPrefix) &&
		strings.HasSuffix(line, verifyReflectionHintSuffix)
}

// isOnlyRedactionPlaceholders reports whether text is made up solely of the
// placeholders llm.RedactText substitutes for secrets and emails. A FAIL whose
// output was entirely a redacted value carries no usable diagnosis.
func isOnlyRedactionPlaceholders(text string) bool {
	for _, placeholder := range []string{"[redacted]", "[REDACTED]", "[email]"} {
		text = strings.ReplaceAll(text, placeholder, "")
	}
	return strings.TrimSpace(text) == ""
}

// VerifyResult is one check outcome for FormatVerifyResults.
type VerifyResult struct {
	Label  string
	Passed bool
	Output string
}

// FormatVerifyResults batches several checks into one bounded feedback string.
// Each result is rendered by VerificationFeedback; the joined text is clipped
// to maxBytes keeping the tail, so a long series of failures cannot blow the
// context window. The most recent (last) failures are the ones kept.
func FormatVerifyResults(results []VerifyResult, maxBytes int) string {
	if len(results) == 0 {
		return ""
	}
	parts := make([]string, 0, len(results))
	for _, result := range results {
		parts = append(parts, VerificationFeedback(result.Label, result.Passed, result.Output, maxBytes))
	}
	return clipVerifyTail(strings.Join(parts, "\n"), resolveVerifyMaxBytes(maxBytes))
}

func resolveVerifyMaxBytes(maxBytes int) int {
	if maxBytes <= 0 {
		return defaultVerifyMaxBytes
	}
	return maxBytes
}

// clipVerifyTail keeps only the last maxBytes bytes of text, prefixed with a
// truncation marker. The kept slice starts on a rune boundary so the result is
// always valid UTF-8, even when the cut would land inside a multibyte rune.
func clipVerifyTail(text string, maxBytes int) string {
	if maxBytes <= 0 || len(text) <= maxBytes {
		return text
	}
	if len(verifyTruncatedMarker) >= maxBytes {
		return clipTailBytes(verifyTruncatedMarker, maxBytes)
	}
	return verifyTruncatedMarker + clipTailBytes(text, maxBytes-len(verifyTruncatedMarker))
}

// clipTailBytes returns the last maxBytes bytes of text, moving the start
// forward to the next rune boundary when it would split a UTF-8 sequence.
func clipTailBytes(text string, maxBytes int) string {
	if maxBytes <= 0 || len(text) <= maxBytes {
		return text
	}
	start := len(text) - maxBytes
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	return text[start:]
}
