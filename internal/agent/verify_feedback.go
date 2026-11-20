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
func VerificationFeedback(label string, passed bool, output string, maxBytes int) string {
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
	return head + "\n" + clipVerifyTail(clean, resolveVerifyMaxBytes(maxBytes))
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
