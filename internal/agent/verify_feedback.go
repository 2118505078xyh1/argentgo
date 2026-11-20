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

	// verifyFixInstruction opens the next model turn after an actionable verify
	// FAIL. It mirrors the aider-style "fix errors below" preamble: the verdict
	// is a request to repair, not merely a status line. Kept as a const so
	// HasDiagnosticContent can strip it as boilerplate.
	verifyFixInstruction = "Fix the errors below, if possible."
	// verifyAnchorExplanation introduces a █-marked source window. It is added
	// only when the feedback actually contains one, so a plain log tail gets no
	// unexplained marker noise.
	verifyAnchorExplanation = "Relevant line(s) marked with █."

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
// VerificationFeedback deliberately does NOT anchor source lines and does NOT
// add the fix-instruction preamble: its output stays byte-for-byte identical to
// the pre-anchoring behaviour. Callers that own the model-facing next turn (the
// verify loop) should call VerificationFeedbackAt directly, so a FAIL also
// carries the "fix errors below" instruction and █-marked source windows.
func VerificationFeedback(label string, passed bool, output string, maxBytes int) string {
	return renderVerifyFeedback(label, passed, output, maxBytes, "", false)
}

// VerificationFeedbackAt renders the same bounded, redacted feedback as
// VerificationFeedback, with two clarity enhancements for an actionable FAIL:
//
//   - the text opens with verifyFixInstruction, so the model reads the verdict
//     as a request to repair rather than as a bare status line;
//   - when root yields a usable file:line reference, verifyAnchorExplanation is
//     emitted immediately before the █-marked source window.
//
// The explanation is gated on the window actually containing █, so a plain log
// tail never gains an unexplained marker header. The whole output portion
// (tail + separator + explanation + anchor windows) stays within maxBytes: a
// share of the budget is reserved for the anchor so it can never crowd out or
// exceed the bound.
//
// A PASS or an empty output still returns just the verdict line. A FAIL with no
// usable anchor (including an unreadable/out-of-root file) gets the fix
// instruction but no █ explanation.
//
// App-side wiring (owned by internal/app, not this file): NewVerifyFunc in
// internal/app/verify_wire.go already captures `root` (it is used as
// command.Dir). In the Verify callback returned by NewVerifyFunc, compose the
// feedback with VerificationFeedbackAt, e.g.
//
//	fb := agent.VerificationFeedbackAt(label, passed, output, maxBytes, root)
//
// and pass `root` through the verify loop (Options -> verifyAfterBatch ->
// BuildVerifyFeedback -> VerificationFeedbackAt) so the model sees the source
// windows instead of only the log tail.
func VerificationFeedbackAt(label string, passed bool, output string, maxBytes int, root string) string {
	return renderVerifyFeedback(label, passed, output, maxBytes, root, true)
}

// renderVerifyFeedback is the shared implementation. fixPreamble is true only
// for VerificationFeedbackAt; VerificationFeedback keeps the original layout
// for callers that are not injecting the model-facing next turn.
func renderVerifyFeedback(label string, passed bool, output string, maxBytes int, root string, fixPreamble bool) string {
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

	prefix := head + "\n"
	if fixPreamble {
		prefix = verifyFixInstruction + "\n" + prefix
	}
	plain := prefix + clipVerifyTail(clean, budget)

	// An empty root (the legacy default) or output with no usable anchor leaves
	// the feedback without a source window, so the █ explanation is omitted too.
	if strings.TrimSpace(root) == "" {
		return plain
	}
	anchor := AnchorSourceLines(clean, root, verifyAnchorPad)
	if anchor == "" {
		return plain
	}

	// Reserve at most half the budget for source windows so the log tail keeps
	// room, then give the remainder (minus the separator and the optional
	// explanation line) back to the tail.
	anchorRoom := budget / 2
	if anchorRoom <= 0 {
		return plain
	}
	anchor = clipTailBytes(anchor, anchorRoom)
	if anchor == "" {
		return plain
	}

	// A clipped window can lose its marked line; only explain █ when one remains.
	explanation := ""
	if strings.Contains(anchor, "█") {
		explanation = verifyAnchorExplanation + "\n"
	}
	tailRoom := budget - len(verifyAnchorSep) - len(explanation) - len(anchor)
	if tailRoom < 0 {
		return plain
	}
	return prefix + clipVerifyTail(clean, tailRoom) + verifyAnchorSep + explanation + anchor
}

// HasDiagnosticContent reports whether a verification feedback string carries
// any substantive diagnostic information beyond the boilerplate that is always
// present: the "[verify: X] PASS/FAIL" verdict header, the fix-instruction and
// █-explanation lines added by VerificationFeedbackAt, the reflection hint,
// redaction placeholders ([redacted]/[REDACTED]/[email]) and blank lines.
//
// It is pure and deliberately conservative: any surviving non-boilerplate
// line -- an error message, a file:line reference, CJK prose, a █-anchored
// source window -- makes it return true. A caller can therefore use a false
// result to detect the aider-P2 case: a FAIL that spent a reflection turn on a
// feedback message the model cannot act on (header + boilerplate only).
//
// It works both on a raw verify output and on the full string returned by
// BuildVerifyFeedback/VerificationFeedbackAt, because it strips the known
// boilerplate wherever it appears rather than assuming a fixed layout.
func HasDiagnosticContent(feedback string) bool {
	for _, line := range strings.Split(feedback, "\n") {
		text := strings.TrimSpace(line)
		if text == "" || isVerifyVerdictLine(text) || isVerifyReflectionHintLine(text) ||
			text == verifyFixInstruction || text == verifyAnchorExplanation {
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
