package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

func TestEventIsTerminalOnlyForStop(t *testing.T) {
	if !(Event{Kind: EventStop}).IsTerminal() {
		t.Fatal("EventStop must be terminal (must-deliver)")
	}
	for _, kind := range []EventKind{
		EventTurnStart, EventTurnEnd, EventCheckpointReady,
		EventTextDelta, EventReasoningDelta, EventToolStart, EventToolEnd,
		EventUsage, EventError, EventNotice,
		EventSubagentStart, EventSubagentEnd,
	} {
		if (Event{Kind: kind}).IsTerminal() {
			t.Errorf("kind %q must not be terminal", kind)
		}
	}
}

// TestRunEmitsExactlyOneTerminalAcrossExitPaths locks in the run-level
// invariant behind "keep only the last terminal": each of the loop's many exit
// paths emits its EventStop and returns immediately, so a subscriber observes a
// single completion event per run and never has to guess which one is final.
func TestRunEmitsExactlyOneTerminalAcrossExitPaths(t *testing.T) {
	t.Run("complete", func(t *testing.T) {
		chat := &fakeChatter{fn: func(int, []llm.Message, []llm.ToolSpec, func(string)) (llm.Message, error) {
			return textReply("done"), nil
		}}
		assertOneTerminal(t, Options{Model: "m", Chatter: chat}, context.Background())
	})
	t.Run("provider error", func(t *testing.T) {
		chat := &fakeChatter{fn: func(int, []llm.Message, []llm.ToolSpec, func(string)) (llm.Message, error) {
			return llm.Message{}, errors.New("boom")
		}}
		assertOneTerminal(t, Options{Model: "m", Chatter: chat}, context.Background())
	})
	t.Run("cancelled", func(t *testing.T) {
		chat := &fakeChatter{fn: func(int, []llm.Message, []llm.ToolSpec, func(string)) (llm.Message, error) {
			return textReply("done"), nil
		}}
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // stop before the first turn so the top-of-loop abort path runs
		assertOneTerminal(t, Options{Model: "m", Chatter: chat}, ctx)
	})
}

func assertOneTerminal(t *testing.T, opts Options, ctx context.Context) {
	t.Helper()
	var emitted []Event
	opts.Emit = func(e Event) { emitted = append(emitted, e) }
	_, _ = (&Runner{Opts: opts}).Run(ctx, "hi")

	terminals := 0
	for _, e := range emitted {
		if e.IsTerminal() {
			terminals++
		}
	}
	if terminals != 1 {
		t.Fatalf("run emitted %d terminal event(s), want 1: %+v", terminals, emitted)
	}
}
