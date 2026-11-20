package agent

import (
	"strings"
	"sync"
)

// DefaultMistakeLimit is the consecutive-mistake threshold used when a
// MistakeTracker is built without an explicit limit.
const DefaultMistakeLimit = 3

// MistakeKind classifies a single failure so the recovery guidance can be
// tailored to what actually went wrong. The three standard values mirror the
// failures an agent loop can observe: a provider/API error, a malformed tool
// call the model produced, and a tool that ran but reported failure.
type MistakeKind string

const (
	MistakeKindAPIError            MistakeKind = "api_error"
	MistakeKindInvalidToolCall     MistakeKind = "invalid_tool_call"
	MistakeKindToolExecutionFailed MistakeKind = "tool_execution_failed"
)

// Valid reports whether k is one of the recognized mistake kinds. An empty or
// unknown kind is ignored by Record so a caller passing a zero value cannot
// inflate the consecutive count by accident.
func (k MistakeKind) Valid() bool {
	switch k {
	case MistakeKindAPIError, MistakeKindInvalidToolCall, MistakeKindToolExecutionFailed:
		return true
	default:
		return false
	}
}

// MistakeGuidance builds a recovery instruction for the current run. It is
// injected at construction (NewMistakeTracker) or later (SetGuidance), keeping
// the tracker a pure state machine with no knowledge of prompts or locales.
type MistakeGuidance func(kind MistakeKind, count int) string

// MistakeAction is the decision returned once the consecutive limit is hit.
// When Stop is false the run continues with Guidance (an injected recovery
// instruction); when Stop is true no guidance was available and the run must
// end. Kind and Count describe the streak that triggered the action so callers
// can log or emit it without re-reading the tracker.
type MistakeAction struct {
	Stop     bool
	Guidance string
	Kind     MistakeKind
	Count    int
}

// MistakeTracker counts consecutive mistakes across all kinds until Reset,
// which a caller invokes after a successful round. It is deliberately separate
// from the repeat guard: the guard detects the *same* call repeated, while this
// tracker detects a streak of *different* failures that keeps the run from
// wandering through unrelated errors. It is safe for concurrent use, so tools
// running in parallel may record failures without external locking.
type MistakeTracker struct {
	mu       sync.Mutex
	limit    int
	count    int
	lastKind MistakeKind
	byKind   map[MistakeKind]int
	guidance MistakeGuidance
}

// NewMistakeTracker builds a tracker with the given consecutive limit (values
// <= 0 select DefaultMistakeLimit). An optional guidance callback is injected
// at construction; it may also be set later with SetGuidance.
func NewMistakeTracker(limit int, guidance ...MistakeGuidance) *MistakeTracker {
	if limit <= 0 {
		limit = DefaultMistakeLimit
	}
	t := &MistakeTracker{
		limit:  limit,
		byKind: make(map[MistakeKind]int, 3),
	}
	if len(guidance) > 0 {
		t.guidance = guidance[0]
	}
	return t
}

// SetGuidance injects or replaces the recovery-guidance callback. Passing nil
// removes it, in which case Check stops the run when the limit is reached.
func (t *MistakeTracker) SetGuidance(fn MistakeGuidance) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.guidance = fn
}

// Record adds one mistake of the given kind to the current streak. Empty or
// unrecognized kinds are ignored. Record is safe to call from several
// goroutines.
func (t *MistakeTracker) Record(kind MistakeKind) {
	if t == nil || !kind.Valid() {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.count++
	t.lastKind = kind
	t.byKind[kind]++
}

// Count returns the length of the current consecutive streak.
func (t *MistakeTracker) Count() int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.count
}

// CountFor returns how many mistakes of kind k occurred in the current streak
// (i.e. since the last Reset).
func (t *MistakeTracker) CountFor(k MistakeKind) int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.byKind[k]
}

// Check reports whether the streak has reached the configured limit. Below the
// limit it returns (false, zero action). At or above the limit it returns an
// action whose Kind/Count describe the streak: if a guidance callback is set
// and returns a non-empty instruction, the run continues with that guidance;
// otherwise the run must stop. The decision is re-evaluated on every call until
// Reset is invoked, so the caller stays in control of when the streak clears.
// The guidance callback is invoked outside the lock to avoid deadlocks if it
// calls back into the tracker.
func (t *MistakeTracker) Check() (shouldStop bool, action MistakeAction) {
	if t == nil {
		return false, MistakeAction{}
	}
	t.mu.Lock()
	if t.count < t.limit || t.limit <= 0 {
		t.mu.Unlock()
		return false, MistakeAction{}
	}
	kind, count, fn := t.lastKind, t.count, t.guidance
	t.mu.Unlock()

	action = MistakeAction{Kind: kind, Count: count}
	if fn != nil {
		if text := strings.TrimSpace(fn(kind, count)); text != "" {
			action.Guidance = text
			return false, action
		}
	}
	action.Stop = true
	return true, action
}

// Reset clears the consecutive streak and its per-kind breakdown. Injected
// guidance and the configured limit are preserved; callers typically invoke
// Reset after a successful round so the next failure starts a fresh streak.
func (t *MistakeTracker) Reset() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.count = 0
	t.lastKind = ""
	for k := range t.byKind {
		delete(t.byKind, k)
	}
}
