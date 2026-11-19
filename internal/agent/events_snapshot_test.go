package agent

import "testing"

// TestRunStateEventsSnapshotIsIndependentCopy checks that eventsSnapshot hands
// out a copy: mutating the underlying event slice afterwards must not change
// what the caller already received.
func TestRunStateEventsSnapshotIsIndependentCopy(t *testing.T) {
	s := &runState{}

	s.eventsMu.Lock()
	s.events = append(s.events,
		Event{Kind: EventTurnStart, Turn: 1, Text: "first"},
		Event{Kind: EventNotice, Text: "second"},
	)
	s.eventsMu.Unlock()

	snap := s.eventsSnapshot()
	if len(snap) != 2 {
		t.Fatalf("snapshot length = %d, want 2", len(snap))
	}
	if snap[0].Text != "first" || snap[1].Text != "second" {
		t.Fatalf("snapshot = %+v, want first and second events", snap)
	}

	// Mutate the original the way a later emit would: edit an existing entry
	// and append a new one, both under eventsMu.
	s.eventsMu.Lock()
	s.events[0].Text = "changed"
	s.events = append(s.events, Event{Kind: EventStop, Reason: StopComplete})
	s.eventsMu.Unlock()

	if len(snap) != 2 {
		t.Fatalf("snapshot length changed to %d after appending to the source", len(snap))
	}
	if snap[0].Text != "first" {
		t.Fatalf("snapshot[0].Text = %q, want %q (source was mutated)", snap[0].Text, "first")
	}
}
