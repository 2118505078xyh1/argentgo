package app

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/agent"
)

// DefaultDeltaBatchWindow is the suggested aggregation window for the TUI. It
// is deliberately shorter than a headless/SSE bridge window (opencode uses
// 100ms): a full-screen terminal should reveal the first token within roughly
// two 60Hz frames, so 32ms keeps batching useful without feeling laggy.
//
// The value is a recommendation for a caller's time.Ticker, not used by the
// pure batcher itself (which is flushed explicitly by its driver).
const DefaultDeltaBatchWindow = 32 * time.Millisecond

const (
	defaultBatchMaxDeltas = 64   // cap deltas per merged event
	defaultBatchMaxRunes  = 4096 // cap runes per merged event (not bytes: CJK/emoji safe)
)

// deltaBatcher merges a burst of agent.EventTextDelta values into a single
// Event whose Text is the concatenation of the burst. It is pure aggregation
// logic: it owns no goroutine, channel or timer, and a caller drives when work
// is released by calling Flush (e.g. from a time.Ticker, see runDeltaBatchTimer).
//
// Contract:
//   - Only EventTextDelta with non-empty Text participates. Other kinds and
//     empty deltas are ignored (Add reports emit=false, state unchanged).
//   - Text is concatenated in Add order, so the merged event's Text is
//     byte-for-byte what the same deltas would produce if forwarded one by one.
//     No rune is ever split: whole strings are appended to a strings.Builder.
//   - Turn/Depth are inherited from the first delta of a burst. A delta with a
//     different Turn or Depth flushes the current burst first, keeping a burst
//     homogeneous and its boundary observationally identical to the raw stream.
//   - maxDeltas / maxRunes are *soft* flush triggers measured between deltas;
//     a single delta larger than maxRunes is emitted on its own (it is never
//     truncated or split), which still bounds how long a burst can grow.
//
// The batcher is not safe for concurrent use. Its single-goroutine driver
// (runDeltaBatchTimer) calls Add and Flush from the same goroutine; do not call
// them from different goroutines without external synchronisation.
//
// It is also intentionally NOT wired into StartTUIEventBridge: see the wiring
// report. Left unconnected, the existing bridge events are unchanged.
type deltaBatcher struct {
	maxDeltas int
	maxRunes  int

	buf         strings.Builder
	count       int // deltas in the current burst
	runes       int // runes in the current burst
	turn        int
	depth       int
	havePending bool
}

// newDeltaBatcher builds a batcher with the given limits. Non-positive limits
// fall back to conservative defaults, so a caller cannot accidentally create
// an unbounded aggregator.
func newDeltaBatcher(maxDeltas, maxRunes int) *deltaBatcher {
	if maxDeltas <= 0 {
		maxDeltas = defaultBatchMaxDeltas
	}
	if maxRunes <= 0 {
		maxRunes = defaultBatchMaxRunes
	}
	return &deltaBatcher{maxDeltas: maxDeltas, maxRunes: maxRunes}
}

// Add folds one event into the current burst. It returns a merged Event with
// emit=true when adding e would exceed a limit (or cross a Turn/Depth
// boundary): that flushes the accumulated burst, and e seeds the next one.
// Otherwise the event is buffered and emit=false is returned.
func (b *deltaBatcher) Add(e agent.Event) (out agent.Event, emit bool) {
	if e.Kind != agent.EventTextDelta || e.Text == "" {
		return agent.Event{}, false
	}

	n := utf8.RuneCountInString(e.Text)
	if b.havePending {
		crossesBoundary := e.Turn != b.turn || e.Depth != b.depth
		hitsLimit := b.count >= b.maxDeltas || b.runes+n > b.maxRunes
		if crossesBoundary || hitsLimit {
			out, emit = b.Flush()
			b.start(e, n)
			return out, emit
		}
	}

	if !b.havePending {
		b.turn, b.depth = e.Turn, e.Depth
		b.havePending = true
	}
	b.buf.WriteString(e.Text)
	b.count++
	b.runes += n
	return agent.Event{}, false
}

// start seeds a fresh burst from e. Callers must have flushed the prior burst
// (if any) before calling start.
func (b *deltaBatcher) start(e agent.Event, n int) {
	b.turn, b.depth = e.Turn, e.Depth
	b.buf.Reset()
	b.buf.WriteString(e.Text)
	b.count, b.runes, b.havePending = 1, n, true
}

// Flush releases the pending burst as one EventTextDelta. ok is false when
// nothing is pending, in which case the returned event is the zero value.
func (b *deltaBatcher) Flush() (out agent.Event, ok bool) {
	if !b.havePending {
		return agent.Event{}, false
	}
	out = agent.Event{
		Kind:  agent.EventTextDelta,
		Turn:  b.turn,
		Depth: b.depth,
		Text:  b.buf.String(),
	}
	b.buf.Reset()
	b.count, b.runes, b.havePending = 0, 0, false
	return out, true
}

// Reset drops the pending burst without emitting it. Useful when a run is
// cancelled and the tail must not be shown.
func (b *deltaBatcher) Reset() {
	b.buf.Reset()
	b.count, b.runes, b.havePending = 0, 0, false
}

// Pending reports whether a burst is currently buffered.
func (b *deltaBatcher) Pending() bool { return b.havePending }

// Count reports the number of deltas currently buffered (for tests/diagnostics).
func (b *deltaBatcher) Count() int { return b.count }

// Runes reports the rune length of the currently buffered text.
func (b *deltaBatcher) Runes() int { return b.runes }

// runDeltaBatchTimer is the reference timer driver for deltaBatcher. It owns a
// single batcher on one goroutine, reads deltas from in, emits merged events via
// emit, and flushes on three triggers: the configured limit (via Add), each
// window tick, and shutdown (ctx cancelled or in closed) for the tail.
//
// Because Add and Flush run on this one goroutine, no locking is needed. It is
// intentionally not wired into the production bridge; see the wiring report.
func runDeltaBatchTimer(ctx context.Context, in <-chan agent.Event, window time.Duration, emit func(agent.Event)) {
	b := newDeltaBatcher(0, 0)
	if window <= 0 {
		window = DefaultDeltaBatchWindow
	}
	ticker := time.NewTicker(window)
	defer ticker.Stop()

	flush := func() {
		if out, ok := b.Flush(); ok {
			emit(out)
		}
	}
	for {
		select {
		case <-ctx.Done():
			flush()
			return
		case e, open := <-in:
			if !open {
				flush()
				return
			}
			if out, ok := b.Add(e); ok {
				emit(out)
			}
		case <-ticker.C:
			flush()
		}
	}
}
