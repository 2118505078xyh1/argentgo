package tui

import (
	"fmt"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// Stream-error visibility helpers (goose study items 3 and 5).
//
// The renderer already knows how to badge any AttemptKind (see
// Model.attemptLinesLocked) and the locale table already ships the
// attempt_stream_error / attempt_retried labels. What was missing was a producer:
// stream errors reached HandleEvent but were ignored, and backoff had no event
// at all. These helpers build the transcript entries that producer needs. They
// are pure — no I/O, no model mutation — so callers decide when to append.

const (
	// maxStreamErrorDetail bounds the text kept in a stream-error notice. The
	// renderer clips again to the terminal width, but the model must not pin an
	// arbitrarily large provider error until the process exits.
	maxStreamErrorDetail = 240
	// maxRetryReason bounds the free-form reason in a retry notice so a hostile
	// or malformed producer cannot widen the transcript with one event.
	maxRetryReason = 120
)

// StreamErrorEntry builds the non-committed notice for a failed LLM stream.
//
// The detail is collapsed to one line, redacted with the supplied function
// (normally llm.RedactText), and bounded to maxStreamErrorDetail grapheme cells.
// A nil redact function leaves the text unredacted, which keeps the helper
// easy to exercise in table-driven tests. AttemptStreamError drives the existing
// badge renderer; no new paint path is involved.
func StreamErrorEntry(detail string, redact func(string) string) Entry {
	if redact != nil {
		detail = redact(detail)
	}
	return Entry{
		Kind:    EntryNotice,
		Attempt: AttemptStreamError,
		Text:    Truncate(collapseSpace(detail), maxStreamErrorDetail),
	}
}

// RetryNotice formats one line of live backoff feedback:
//
//	⟳ retry <reason> in <waitMS>ms (attempt/max)
//
// The reason is redacted through llm.RedactText, collapsed, and bounded; the
// counts are clamped to non-negative values so the string is stable for
// malformed callers. The leading mark is part of the text because this notice
// is appended as a plain EntryNotice: the renderer draws one badge per entry,
// and a second attempt badge here would duplicate the "⟳" glyph.
func RetryNotice(reason string, attempt, max int, waitMS int64) string {
	reason = Truncate(collapseSpace(llm.RedactText(reason)), maxRetryReason)
	if reason == "" {
		reason = "stream"
	}
	if attempt < 0 {
		attempt = 0
	}
	if max < 0 {
		max = 0
	}
	if waitMS < 0 {
		waitMS = 0
	}
	return fmt.Sprintf("⟳ retry %s in %dms (%d/%d)", reason, waitMS, attempt, max)
}
