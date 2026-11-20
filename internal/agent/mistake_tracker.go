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

// Invalid-argument circuit breaker.
//
// This complements MistakeTracker rather than replacing it. The tracker counts
// *all* consecutive failures and, because guidance is normally injected, only
// re-feeds the model; it never forces a hard stop. The breaker is deliberately
// narrow: it counts only arguments a tool rejected as invalid (an
// InvalidArgumentsError in the kilocode report). Three such rejections in one
// user turn mean the model is not learning from the validation feedback, so the
// breaker signals that the turn must end. A single successful call or any
// failure that is NOT an argument-validation error proves progress and clears
// the count immediately.
//
// The caller owns classification (which tool errors are "invalid arguments")
// and the user-turn boundary (when to call Reset explicitly); this type is a
// pure, concurrency-safe counter with no knowledge of tools or events.

// DefaultInvalidArgThreshold is the trip point used when the breaker is built
// without an explicit (positive) threshold.
const DefaultInvalidArgThreshold = 3

// InvalidArgBreakerCap bounds the internal counter so a stream of invalid-
// argument results cannot grow it without limit. Once the counter reaches the
// cap it saturates; ShouldStop stays true until Reset. A requested threshold
// higher than the cap is clamped down to the cap so the breaker always remains
// trippable.
const InvalidArgBreakerCap = 64

// InvalidArgOutcome is the breaker's view of one tool outcome. Keeping the
// classification at the caller lets dispatch reuse its existing error
// inspection (tool validation errors, i18n "error.invalid_args", etc.) without
// this type depending on tools, i18n or error strings.
type InvalidArgOutcome uint8

const (
	// InvalidArgOutcomeNone is a neutral outcome (e.g. an unrun, guarded or
	// cancelled call). It neither counts nor clears — the call never reached a
	// tool, so it says nothing about the model's argument quality.
	InvalidArgOutcomeNone InvalidArgOutcome = iota
	// InvalidArgOutcomeInvalidArgs means a tool rejected the arguments as
	// invalid. This is the only outcome that advances the breaker.
	InvalidArgOutcomeInvalidArgs
	// InvalidArgOutcomeSuccess means a tool ran successfully. It clears the
	// count because the model made progress.
	InvalidArgOutcomeSuccess
	// InvalidArgOutcomeOtherFailure means a tool ran but failed for a reason
	// unrelated to argument validation. It clears the count: the constraint is
	// specifically about repeated invalid arguments, and an unrelated failure
	// is not evidence the model is ignoring validation feedback.
	InvalidArgOutcomeOtherFailure
)

// InvalidArgBreaker counts invalid-argument rejections within one user turn and
// reports when the configured threshold is reached. It is safe for concurrent
// use, so tools running in parallel may observe outcomes without an external
// lock.
type InvalidArgBreaker struct {
	mu        sync.Mutex
	threshold int
	count     int
}

// NewInvalidArgBreaker builds a breaker with the given threshold. A
// non-positive threshold selects DefaultInvalidArgThreshold; a threshold above
// InvalidArgBreakerCap is clamped to the cap.
func NewInvalidArgBreaker(threshold int) *InvalidArgBreaker {
	if threshold <= 0 {
		threshold = DefaultInvalidArgThreshold
	}
	if threshold > InvalidArgBreakerCap {
		threshold = InvalidArgBreakerCap
	}
	return &InvalidArgBreaker{threshold: threshold}
}

// Observe folds one tool outcome into the breaker and returns whether the
// threshold has been reached. It is the single entry point intended for
// dispatch: classify the call result, pass it here, and stop the turn when the
// result is true. The decision is re-evaluated on every call until Reset, so
// the caller stays in control of when the count clears.
func (b *InvalidArgBreaker) Observe(outcome InvalidArgOutcome) (shouldStop bool) {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	switch outcome {
	case InvalidArgOutcomeInvalidArgs:
		if b.count < InvalidArgBreakerCap {
			b.count++
		}
	case InvalidArgOutcomeSuccess, InvalidArgOutcomeOtherFailure:
		// Both prove progress (or at least are not argument errors) and clear
		// the streak immediately.
		b.count = 0
	case InvalidArgOutcomeNone:
		// Neutral: leave the count untouched.
	}
	return b.count >= b.threshold
}

// RecordInvalidArgs is a convenience wrapper for reporting one
// invalid-argument rejection. It returns the same stop signal as Observe.
func (b *InvalidArgBreaker) RecordInvalidArgs() bool {
	return b.Observe(InvalidArgOutcomeInvalidArgs)
}

// Reset clears the invalid-argument count. A caller typically invokes it at a
// user-turn boundary; success and non-argument failures already reset via
// Observe. The configured threshold is preserved.
func (b *InvalidArgBreaker) Reset() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.count = 0
}

// ShouldStop reports whether the threshold has been reached without recording
// a new outcome.
func (b *InvalidArgBreaker) ShouldStop() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.count >= b.threshold
}

// Count returns the current invalid-argument count (saturated at the cap).
func (b *InvalidArgBreaker) Count() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.count
}

// Threshold returns the effective trip point after defaulting and clamping.
func (b *InvalidArgBreaker) Threshold() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.threshold
}
