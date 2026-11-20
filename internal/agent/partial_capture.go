package agent

import (
	"strings"
	"sync"
	"unicode/utf8"
)

// defaultPartialByteLimit bounds how much streamed text one capture keeps. A
// prefix is what a UI can meaningfully show after a cut, and 8 KiB is far beyond
// a normal answer while still capping the memory a runaway stream can pin. The
// cap is enforced as deltas arrive, not only when Snapshot is called.
const defaultPartialByteLimit = 8 << 10

// partialCapture accumulates the text a single model attempt managed to stream
// before it failed or was cancelled. It is keyed to one model and one step, so
// a fallback switch starts a fresh capture and never mixes two attempts' text.
//
// A mutex guards the builder: a provider's onText callback may run on its own
// goroutine while the loop takes a Snapshot, so the capture must be safe under
// -race without the loop having to coordinate with the stream.
type partialCapture struct {
	mu        sync.Mutex
	model     string
	turn      int
	limit     int
	b         strings.Builder
	truncated bool
}

func newPartialCapture(model string, turn, limit int) *partialCapture {
	if limit <= 0 {
		limit = defaultPartialByteLimit
	}
	return &partialCapture{model: model, turn: turn, limit: limit}
}

// Add appends one streamed delta, keeping at most limit bytes of complete UTF-8
// runes. When a delta would cross the cap, only the longest rune-safe prefix is
// kept and truncated is latched so a consumer can mark the text as incomplete.
func (c *partialCapture) Add(delta string) {
	if delta == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	remaining := c.limit - c.b.Len()
	if remaining <= 0 {
		c.truncated = true
		return
	}
	if len(delta) <= remaining {
		_, _ = c.b.WriteString(delta)
		return
	}
	cut := runeSafePrefixLen(delta, remaining)
	_, _ = c.b.WriteString(delta[:cut])
	c.truncated = true
}

// partialSnapshot is the value handed to the failure boundary. Its Text is an
// independent copy: a later Add cannot mutate text a caller already read.
type partialSnapshot struct {
	Text       string
	StopReason string
	Model      string
	Turn       int
	Truncated  bool
}

// Snapshot returns the text captured so far together with its provenance and
// the stop reason the run is about to report. The stop reason is supplied by
// the caller because the loop owns stop semantics; the capture owns only bytes.
func (c *partialCapture) Snapshot(stopReason string) partialSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return partialSnapshot{
		Text:       strings.Clone(c.b.String()),
		StopReason: stopReason,
		Model:      c.model,
		Turn:       c.turn,
		Truncated:  c.truncated,
	}
}

// runeSafePrefixLen returns the largest n <= maxBytes for which s[:n] ends on a
// UTF-8 rune boundary. Backing off from a continuation byte keeps CJK and emoji
// intact even when the cap lands in the middle of a multi-byte sequence.
func runeSafePrefixLen(s string, maxBytes int) int {
	if maxBytes >= len(s) {
		return len(s)
	}
	n := maxBytes
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return n
}

// partialStopReason maps the failure boundary to the stop reason the loop will
// report: a cancelled context wins over the provider error that carried it.
func partialStopReason(cancelled bool) string {
	if cancelled {
		return StopCancelled
	}
	return StopProviderError
}

// emitPartial publishes a captured prefix as one additive event. It is a no-op
// when nothing was streamed, so a failure that produced no text keeps its old
// wire output (error + stop only). The event is not terminal: the loop still
// emits EventStop as the single run terminal.
func (s *runState) emitPartial(snap partialSnapshot) {
	if snap.Text == "" {
		return
	}
	s.emit(Event{
		Kind:              EventPartial,
		Turn:              snap.Turn,
		Text:              snap.Text,
		Reason:            snap.StopReason,
		Partial:           true,
		PartialStopReason: snap.StopReason,
		PartialModel:      snap.Model,
		PartialTruncated:  snap.Truncated,
	})
}
