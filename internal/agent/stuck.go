package agent

import "strings"

// Stuck detection is derived from OpenHands' StuckDetector: it looks at a
// bounded tail of run events for a repetition loop and offers a one-shot
// corrective nudge instead of silently burning the turn budget. It is a pure
// observer over the existing Event vocabulary — no timers, no I/O, no coupling
// to the loop's dedupe or mistake tracking.

const (
	// stuckWindow is the number of recent events examined. OpenHands uses a
	// 20-event window; the same bound keeps detection local and predictable.
	stuckWindow = 20
	// Thresholds for each recognised pattern.
	stuckRepeatCalls    = 3 // identical tool calls in a row
	stuckErrorStreak    = 5 // consecutive error steps
	stuckAlternationLen = 4 // A/B/A/B... tool calls
	stuckMonologueLen   = 5 // assistant text steps with no tool call
)

// steeringNoticePrefix mirrors the notice injectSteering emits for every user
// message it moves into the conversation. It is the only user message that is
// identifiable from the event stream, so it marks an episode boundary.
const steeringNoticePrefix = "steering: "

// One-shot guidance strings. Kept concise and in English to match the other
// loop notices; the model reads them as a fresh user turn.
const (
	stuckGuidanceRepeat = "Repeating the exact same call again will not work; change the tool, arguments, or approach before retrying."
	stuckGuidanceError  = "Several consecutive errors occurred; stop retrying and diagnose the underlying cause first."
	stuckGuidanceAlt    = "The recent calls keep alternating between two actions without progress; commit to one approach and verify the result."
	stuckGuidanceText   = "A long explanation without tool calls made no progress; take a concrete action or finish the answer."
)

// StuckDetector watches events for repetition loops. It keeps only whether the
// current episode has already been nudged, so guidance is emitted at most once
// per episode until a healthy observation (or Reset) re-arms it.
type StuckDetector struct {
	nudged bool
	// lastNudgedIndex is the number of events seen when nudging fired. It is
	// kept for parity with OpenHands' last-nudged-event dedupe and for debugging
	// the exact point at which a loop was detected.
	lastNudgedIndex int
}

// Reset clears the one-shot state so the next stuck episode can be nudged.
func (d *StuckDetector) Reset() {
	if d == nil {
		return
	}
	d.nudged = false
	d.lastNudgedIndex = 0
}

// Observe examines the recent events and returns corrective guidance the first
// time a stuck pattern is seen in an episode. stuck reports whether a pattern
// is present even when the one-shot has already fired; callers should gate on
// guidance != "" so the nudge is delivered only once.
func (d *StuckDetector) Observe(events []Event) (guidance string, stuck bool) {
	if d == nil {
		return "", false
	}
	window := events
	if len(window) > stuckWindow {
		window = window[len(window)-stuckWindow:]
	}
	if start := lastUserBoundary(window); start >= 0 {
		window = window[start+1:]
	}
	if len(window) == 0 {
		// Nothing to judge yet: treat as healthy so a fresh episode is armed.
		d.nudged = false
		return "", false
	}

	guidance, stuck = detectStuck(window)
	if !stuck {
		// A healthy observation ends the episode and re-arms the one-shot.
		d.nudged = false
		return "", false
	}
	if d.nudged {
		return "", true
	}
	d.nudged = true
	d.lastNudgedIndex = len(events)
	return guidance, true
}

// lastUserBoundary returns the index of the last identifiable user message in
// events, or -1 when there is none. Patterns before that point belong to a
// previous episode and must not trigger a new nudge.
func lastUserBoundary(events []Event) int {
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		if e.Kind == EventNotice && strings.HasPrefix(e.Text, steeringNoticePrefix) {
			return i
		}
	}
	return -1
}

// stuckCall is the identity of one tool call with volatile fields removed: no
// call id, turn number, duration or usage, only what decides "same call".
type stuckCall struct {
	name    string
	args    string
	isError bool
}

type stuckStepKind uint8

const (
	stepOther stuckStepKind = iota
	stepCall
	stepError
	stepText
)

// stuckStep is one meaningful event collapsed to the fields detection needs.
type stuckStep struct {
	kind    stuckStepKind
	name    string
	args    string
	isError bool
}

// detectStuck returns the guidance for the first pattern that matches. Pattern
// order is deliberate: a repeating call is the clearest signal, alternating
// calls next, then a bare error streak, then a tool-free monologue.
func detectStuck(window []Event) (string, bool) {
	steps, calls := buildSteps(window)
	if hasRepeatCall(calls, stuckRepeatCalls) {
		return stuckGuidanceRepeat, true
	}
	if hasAlternation(calls, stuckAlternationLen) {
		return stuckGuidanceAlt, true
	}
	if hasErrorStreak(steps, stuckErrorStreak) {
		return stuckGuidanceError, true
	}
	if hasMonologue(window, stuckMonologueLen) {
		return stuckGuidanceText, true
	}
	return "", false
}

// buildSteps pairs tool_start/tool_end events by call id within the window and
// returns both the full meaningful-step sequence and the tool-call subsequence
// used by the repetition patterns.
func buildSteps(window []Event) ([]stuckStep, []stuckCall) {
	starts := make(map[string]stuckCall, 4)
	steps := make([]stuckStep, 0, len(window))
	calls := make([]stuckCall, 0, len(window)/2)
	for _, e := range window {
		switch e.Kind {
		case EventToolStart:
			starts[e.ToolCallID] = stuckCall{name: e.ToolName, args: e.Args}
		case EventToolEnd:
			c := stuckCall{name: e.ToolName, isError: e.IsError}
			if start, ok := starts[e.ToolCallID]; ok {
				c = start
				c.isError = e.IsError
				delete(starts, e.ToolCallID)
			}
			steps = append(steps, stuckStep{kind: stepCall, name: c.name, args: c.args, isError: c.isError})
			calls = append(calls, c)
		case EventError:
			steps = append(steps, stuckStep{kind: stepError})
		case EventTextDelta:
			steps = append(steps, stuckStep{kind: stepText})
		}
	}
	return steps, calls
}

// sameCall compares only stable identity fields, ignoring ids and timing.
func sameCall(a, b stuckCall) bool {
	return a.name == b.name && a.args == b.args && a.isError == b.isError
}

// hasRepeatCall reports whether minCalls or more adjacent calls are identical.
func hasRepeatCall(calls []stuckCall, minCalls int) bool {
	if len(calls) < minCalls {
		return false
	}
	run := 1
	for i := 1; i < len(calls); i++ {
		if sameCall(calls[i], calls[i-1]) {
			run++
		} else {
			run = 1
		}
		if run >= minCalls {
			return true
		}
	}
	return false
}

// hasAlternation reports whether the call tail alternates between two distinct
// calls (A/B/A/B...) for at least minCalls calls.
func hasAlternation(calls []stuckCall, minCalls int) bool {
	if len(calls) < minCalls {
		return false
	}
	run := 1
	for i := 1; i < len(calls); i++ {
		if i >= 2 && sameCall(calls[i], calls[i-2]) && !sameCall(calls[i], calls[i-1]) {
			run++
		} else {
			run = 2
		}
		if run >= minCalls {
			return true
		}
	}
	return false
}

// isErrorStep reports whether a step represents a failure: a provider error or
// a tool call that returned IsError.
func isErrorStep(s stuckStep) bool {
	return s.kind == stepError || (s.kind == stepCall && s.isError)
}

// hasErrorStreak reports whether minSteps or more consecutive steps failed.
func hasErrorStreak(steps []stuckStep, minSteps int) bool {
	run := 0
	for _, s := range steps {
		if isErrorStep(s) {
			run++
			if run >= minSteps {
				return true
			}
		} else {
			run = 0
		}
	}
	return false
}

// hasMonologue reports whether minSteps or more text deltas occurred with no
// tool call in between. Other event kinds (turn boundaries, usage, notices) are
// neutral and do not break the run.
func hasMonologue(window []Event, minSteps int) bool {
	run := 0
	for _, e := range window {
		switch e.Kind {
		case EventTextDelta:
			if strings.TrimSpace(e.Text) != "" {
				run++
				if run >= minSteps {
					return true
				}
			}
		case EventToolStart, EventToolEnd:
			run = 0
		}
	}
	return false
}
