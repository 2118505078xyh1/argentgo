package server

import (
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/agent"
)

// TestEventHubTerminalPublishNeverBlocksOnConcurrentDrain guards the
// non-blocking delivery contract of the must-deliver terminal path.
//
// A subscriber's channel is filled to capacity, and a consumer drains it
// concurrently. When Publish sees a full channel it must free the reserved
// terminal slot without ever blocking. If it used a plain blocking `<-ch`, a
// consumer that empties the channel in that window would leave Publish stuck
// while holding h.mu — deadlocking every other publisher and subscriber. The
// test publishes many terminals under that contention and fails if any Publish
// does not return promptly.
func TestEventHubTerminalPublishNeverBlocksOnConcurrentDrain(t *testing.T) {
	const (
		rounds    = 40
		terminals = 400
	)
	for round := 0; round < rounds; round++ {
		hub := NewEventHub()
		ch, stop := hub.Subscribe(0)

		// Fill to capacity so each terminal publish takes the "full" branch.
		for i := 0; i < cap(ch); i++ {
			hub.Publish(agent.Event{Kind: agent.EventNotice, Text: "fill"})
		}

		// The consumer drains continuously and can empty the channel at any
		// point, including between Publish's full-channel check and its
		// oldest-event removal.
		drained := make(chan struct{})
		go func() {
			defer close(drained)
			for range ch {
			}
		}()

		published := make(chan struct{})
		go func() {
			defer close(published)
			for i := 0; i < terminals; i++ {
				hub.Publish(agent.Event{Kind: agent.EventStop, Reason: "done"})
			}
		}()

		select {
		case <-published:
		case <-time.After(3 * time.Second):
			stop()
			t.Fatalf("round %d: terminal Publish blocked — hub deadlocked", round)
		}

		stop() // unsubscribes; the drain loop ends once the channel is closed
		select {
		case <-drained:
		case <-time.After(3 * time.Second):
			t.Fatalf("round %d: consumer did not finish draining", round)
		}
	}
}

// TestEventHubTerminalStillDeliveredWhenFull exercises the contract the fix
// must preserve: a terminal reaches a subscriber whose normal quota is full,
// using the reserved slot rather than evicting it.
func TestEventHubTerminalStillDeliveredWhenFull(t *testing.T) {
	hub := NewEventHub()
	ch, stop := hub.Subscribe(0)
	defer stop()

	for i := 0; i < eventSubCap; i++ {
		hub.Publish(agent.Event{Kind: agent.EventNotice, Text: "pending"})
	}
	hub.Publish(agent.Event{Kind: agent.EventStop, Reason: agent.StopComplete})

	if got := hub.Evicted(); got != 0 {
		t.Fatalf("evicted = %d, want 0: a terminal must not evict", got)
	}
	terminals := 0
	for i := 0; i < eventSubCap+1; i++ {
		select {
		case ev := <-ch:
			if ev.Kind == agent.EventStop {
				terminals++
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out draining event %d", i)
		}
	}
	if terminals != 1 {
		t.Fatalf("terminals = %d, want 1", terminals)
	}
}
