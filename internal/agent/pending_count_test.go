package agent

import "testing"

func TestSteeringPendingIsNonDraining(t *testing.T) {
	ch := make(chan string, 2)
	ch <- "first"
	ch <- "second"
	s := &steering{ch: ch}

	if got := s.Pending(); got != 2 {
		t.Fatalf("Pending() = %d, want 2", got)
	}
	if got := len(ch); got != 2 {
		t.Fatalf("len(ch) after Pending() = %d, want 2 (Pending must not consume)", got)
	}

	<-ch // 恰好移除一条排队的纠正消息

	if got := s.Pending(); got != 1 {
		t.Fatalf("Pending() after draining one = %d, want 1", got)
	}
}

func TestSteeringPendingNilSafe(t *testing.T) {
	var s *steering
	if got := s.Pending(); got != 0 {
		t.Fatalf("nil steering Pending() = %d, want 0", got)
	}
	if got := (&steering{}).Pending(); got != 0 {
		t.Fatalf("nil channel Pending() = %d, want 0", got)
	}
}

func TestTurnBoundaryEventsCarryPendingCount(t *testing.T) {
	ch := make(chan string, 1)
	ch <- "queued correction"
	s := &runState{steer: &steering{ch: ch}}

	s.emit(Event{Kind: EventTurnEnd, Turn: 0})
	s.emit(Event{Kind: EventTextDelta, Turn: 0, Text: "partial"})

	if len(s.events) != 2 {
		t.Fatalf("recorded %d events, want 2", len(s.events))
	}
	if got := s.events[0].PendingMessages; got <= 0 {
		t.Fatalf("turn_end PendingMessages = %d, want > 0", got)
	}
	if got := s.events[1].PendingMessages; got != 0 {
		t.Fatalf("text_delta PendingMessages = %d, want 0", got)
	}
	if got := len(ch); got != 1 {
		t.Fatalf("len(ch) after emit = %d, want 1 (emit must be non-draining)", got)
	}
}
