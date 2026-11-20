package agent

import "strings"

// ReasoningOp is the rewrite a frontend must apply to its displayed reasoning
// text. When Replace is set the frontend replaces the whole displayed block
// with Text; otherwise Text is a fragment appended to the tail. Carrying both
// forms in one value lets a stream mix incremental deltas with an authoritative
// end frame that re-sends the complete block.
type ReasoningOp struct {
	Replace bool
	Text    string
}

// MergeReasoningDelta folds one incremental reasoning fragment into the text
// accumulated so far. It returns the new accumulated text and whether the
// frontend must replace (rather than append) to avoid rendering the block
// twice.
//
// Some providers emit cumulative snapshots ("让", "让我", "让我思考") instead
// of strict increments. A fragment that already starts with everything seen so
// far is such a snapshot: the longer one wins and the frontend replaces. An
// exact repeat is a no-op. Strictly new fragments are appended.
//
// The operation is byte-oriented (strings.HasPrefix / concatenation), so a
// multi-byte rune is never split: CJK and emoji text stays intact.
func MergeReasoningDelta(current, delta string) (next string, replace bool) {
	if delta == "" {
		return current, false
	}
	if current == "" {
		return delta, false
	}
	if strings.HasPrefix(delta, current) {
		if len(delta) == len(current) {
			return current, false // exact duplicate: nothing to do
		}
		return delta, true // cumulative snapshot: replace, don't duplicate
	}
	return current + delta, false
}

// ReasoningEndOverride applies the terminal reasoning frame. A provider that
// re-sends the complete block at the end must override whatever the incremental
// frames produced: the end frame is authoritative, never appended. An empty end
// frame leaves the accumulated text untouched and an identical one is a no-op,
// so an idempotent provider does not trigger a redundant repaint.
func ReasoningEndOverride(current, final string) (next string, replace bool) {
	if final == "" {
		return current, false
	}
	if final == current {
		return current, false
	}
	return final, true
}

// ReasoningAggregator is a stateful wrapper around the two pure merge helpers.
// It lets a caller that owns a per-request reasoning stream feed fragments in
// order and translate each returned ReasoningOp into a frontend event. The type
// is pure in-memory state: no I/O, no events and no dependencies, so callers
// keep full control over when and how to emit. A zero value is ready to use.
type ReasoningAggregator struct {
	text string
}

// Add folds one incremental fragment in and returns the op to emit.
func (a *ReasoningAggregator) Add(delta string) ReasoningOp {
	next, replace := MergeReasoningDelta(a.text, delta)
	a.text = next
	if replace {
		return ReasoningOp{Replace: true, Text: next}
	}
	return ReasoningOp{Text: delta}
}

// Finish applies the authoritative end frame and returns the op to emit.
func (a *ReasoningAggregator) Finish(final string) ReasoningOp {
	next, replace := ReasoningEndOverride(a.text, final)
	a.text = next
	if replace {
		return ReasoningOp{Replace: true, Text: next}
	}
	return ReasoningOp{}
}

// Text returns everything accumulated so far (the full displayed block).
func (a *ReasoningAggregator) Text() string {
	return a.text
}
