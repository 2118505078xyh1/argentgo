package agent

import "time"

// timeSource is the wall clock behind event timing. It is a variable so tests
// can inject a deterministic clock; production always uses time.Now.
var timeSource = time.Now

// eventTiming is the wall-clock bracket of one observable step (a tool call or
// a reasoning window). StartedAtMS and FinishedAtMS are absolute Unix-epoch
// milliseconds: unlike the relative DurationMS they stay meaningful after a
// session reload, which is what a frontend needs to rebuild a real thinking or
// tool duration instead of inventing one from the first frame's clock.
type eventTiming struct {
	StartedAtMS  int64
	FinishedAtMS int64
	Elapsed      time.Duration
}

// unixMillis renders t as Unix epoch milliseconds. The zero time renders as 0
// so an event with no timing keeps its new omitempty fields absent, leaving
// existing JSON encodings byte-for-byte unchanged.
func unixMillis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// bracketTiming derives the wire timing from a start/end pair. An incomplete
// or inverted pair yields the zero value, so callers never fabricate a
// timestamp or a duration when one end of the window is missing.
func bracketTiming(start, finish time.Time) eventTiming {
	if start.IsZero() || finish.IsZero() {
		return eventTiming{}
	}
	t := eventTiming{
		StartedAtMS:  unixMillis(start),
		FinishedAtMS: unixMillis(finish),
	}
	if !finish.Before(start) {
		t.Elapsed = finish.Sub(start)
	}
	return t
}

// startTool records the wall-clock start of call id. Parallel workers touch
// distinct ids, so the map is guarded. A nil map is created lazily, which keeps
// manually built runStates (e.g. in tests) safe.
func (s *runState) startTool(id string, at time.Time) {
	s.timingMu.Lock()
	defer s.timingMu.Unlock()
	if s.toolTimes == nil {
		s.toolTimes = make(map[string]time.Time)
	}
	s.toolTimes[id] = at
}

// finishTool consumes the start recorded for id and brackets it with at. An
// unknown id (blocked, cancelled or a manually built state) yields the zero
// value: no timestamp is fabricated and the omitempty fields stay absent.
func (s *runState) finishTool(id string, at time.Time) eventTiming {
	s.timingMu.Lock()
	start, ok := s.toolTimes[id]
	if ok {
		delete(s.toolTimes, id)
	}
	s.timingMu.Unlock()
	if !ok {
		return eventTiming{}
	}
	return bracketTiming(start, at)
}

// startReasoning marks the start of the request that produced a reasoning
// block. Reasoning is emitted on the loop goroutine, but sharing timingMu with
// toolTimes keeps the state race-free against streaming progress callbacks.
func (s *runState) startReasoning(at time.Time) {
	s.timingMu.Lock()
	s.reasoningStart = at
	s.timingMu.Unlock()
}

// finishReasoning closes the current reasoning window and returns its
// wall-clock bracket. Calling it without a prior start is a safe no-op.
func (s *runState) finishReasoning(at time.Time) eventTiming {
	s.timingMu.Lock()
	start := s.reasoningStart
	s.reasoningStart = time.Time{}
	s.timingMu.Unlock()
	return bracketTiming(start, at)
}
