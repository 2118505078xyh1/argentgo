package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// TestToolTimingBracket checks the core calculation: a tool's wall-clock start
// and end become event timestamps and the elapsed value matches exactly. The
// recorded start is consumed once, so a replay cannot duplicate the bracket.
func TestToolTimingBracket(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	finish := start.Add(1500 * time.Millisecond)
	s := &runState{}

	s.startTool("c1", start)
	got := s.finishTool("c1", finish)
	if got.StartedAtMS != start.UnixMilli() {
		t.Fatalf("StartedAtMS = %d, want %d", got.StartedAtMS, start.UnixMilli())
	}
	if got.FinishedAtMS != finish.UnixMilli() {
		t.Fatalf("FinishedAtMS = %d, want %d", got.FinishedAtMS, finish.UnixMilli())
	}
	if got.Elapsed != 1500*time.Millisecond {
		t.Fatalf("Elapsed = %v, want 1.5s", got.Elapsed)
	}
	if again := s.finishTool("c1", finish); again != (eventTiming{}) {
		t.Fatalf("second finishTool = %+v, want zero (start must be consumed)", again)
	}
}

// TestReasoningTimingBracket checks the reasoning window: it returns the real
// request-to-reply bracket and resets, so a later call without a start is a
// safe no-op rather than a fabricated duration.
func TestReasoningTimingBracket(t *testing.T) {
	start := time.Unix(1_700_000_100, 0)
	finish := start.Add(275 * time.Millisecond)
	s := &runState{}

	s.startReasoning(start)
	got := s.finishReasoning(finish)
	if got.StartedAtMS != start.UnixMilli() || got.FinishedAtMS != finish.UnixMilli() {
		t.Fatalf("reasoning bracket = %+v", got)
	}
	if got.Elapsed != 275*time.Millisecond {
		t.Fatalf("reasoning Elapsed = %v, want 275ms", got.Elapsed)
	}
	if again := s.finishReasoning(finish); again != (eventTiming{}) {
		t.Fatalf("finishReasoning without start = %+v, want zero", again)
	}
	// An inverted pair never produces a positive duration.
	s.startReasoning(finish)
	if inverted := s.finishReasoning(start); inverted.Elapsed != 0 {
		t.Fatalf("inverted reasoning Elapsed = %v, want 0", inverted.Elapsed)
	}
}

// TestTimingNilRunState proves timing is a no-op on a zero-value runState, the
// shape manually built by many tests: the map is created lazily and unknown ids
// or a missing reasoning start never fabricate a timestamp.
func TestTimingNilRunState(t *testing.T) {
	var s runState
	now := time.Unix(1_700_000_200, 0)
	if got := s.finishTool("missing", now); got != (eventTiming{}) {
		t.Fatalf("finishTool on nil state = %+v, want zero", got)
	}
	if got := s.finishReasoning(now); got != (eventTiming{}) {
		t.Fatalf("finishReasoning on nil state = %+v, want zero", got)
	}
	s.startTool("c1", now)
	if got := s.finishTool("c1", now.Add(time.Second)); got.Elapsed != time.Second {
		t.Fatalf("lazy map Elapsed = %v, want 1s", got.Elapsed)
	}
}

// TestToolTimingConcurrentIndependent runs many calls through the same state
// at once. Each id must keep its own start and produce its own bracket; the
// map must be empty afterwards. This is the race-detector target for timingMu.
func TestToolTimingConcurrentIndependent(t *testing.T) {
	const n = 32
	s := &runState{}
	base := time.Unix(1_700_000_300, 0)
	errs := make(chan string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		i := i
		id := fmt.Sprintf("c%d", i)
		start := base.Add(time.Duration(i) * 10 * time.Millisecond)
		wantElapsed := time.Duration(i+1) * 7 * time.Millisecond
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.startTool(id, start)
			got := s.finishTool(id, start.Add(wantElapsed))
			if got.Elapsed != wantElapsed ||
				got.StartedAtMS != start.UnixMilli() ||
				got.FinishedAtMS != start.Add(wantElapsed).UnixMilli() {
				errs <- fmt.Sprintf("call %s bracket = %+v, want start=%d elapsed=%v", id, got, start.UnixMilli(), wantElapsed)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Error(msg)
	}
	s.timingMu.Lock()
	left := len(s.toolTimes)
	s.timingMu.Unlock()
	if left != 0 {
		t.Fatalf("toolTimes has %d leftover entries, want 0", left)
	}
}

// TestTimingZeroValueKeepsEventJSON guards the additive-field promise: an
// event that carries no timing must marshal without any new keys, so existing
// session/RPC JSON stays byte-for-byte unchanged.
func TestTimingZeroValueKeepsEventJSON(t *testing.T) {
	data, err := json.Marshal(Event{Kind: EventToolEnd, Turn: 3, ToolCallID: "c", DurationMS: 42})
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, key := range []string{"started_at_ms", "finished_at_ms", "StartedAtMS", "FinishedAtMS"} {
		if strings.Contains(text, key) {
			t.Fatalf("zero timing leaked into event JSON: %s", text)
		}
	}
}

// TestTimingFieldsPopulatedOnEmit verifies the emit wiring end to end: a
// reasoning block and a tool call both receive absolute, ordered timestamps on
// the events a frontend actually consumes.
func TestTimingFieldsPopulatedOnEmit(t *testing.T) {
	chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n == 1 {
			reply := toolReply(toolCall("c1", "read", `{}`))
			reply.Reasoning = "思考一下"
			return reply, nil
		}
		return textReply("done"), nil
	}}
	sink := &eventSink{}
	runner := &Runner{Opts: Options{
		Model:    "m",
		Chatter:  chat,
		Tools:    &fakeTools{specs: []llm.ToolSpec{toolSpec("read")}},
		MaxTurns: 3,
		Emit:     sink.emit,
	}}
	if _, err := runner.Run(context.Background(), "task"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	sink.mu.Lock()
	events := append([]Event(nil), sink.events...)
	sink.mu.Unlock()
	var reasoning, start, end *Event
	for i := range events {
		switch events[i].Kind {
		case EventReasoningDelta:
			if reasoning == nil {
				reasoning = &events[i]
			}
		case EventToolStart:
			if start == nil {
				start = &events[i]
			}
		case EventToolEnd:
			if end == nil {
				end = &events[i]
			}
		}
	}
	if reasoning == nil || reasoning.StartedAtMS <= 0 || reasoning.FinishedAtMS < reasoning.StartedAtMS {
		t.Fatalf("reasoning_delta timing = %+v", reasoning)
	}
	if start == nil || start.StartedAtMS <= 0 {
		t.Fatalf("tool_start timing = %+v", start)
	}
	if end == nil || end.StartedAtMS <= 0 || end.FinishedAtMS < end.StartedAtMS {
		t.Fatalf("tool_end timing = %+v", end)
	}
}
