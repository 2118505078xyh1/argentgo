package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/agent"
)

// TestSubscribeAfterReturnsAtomicCursor checks the resume-cursor contract of
// SubscribeAfter: the returned seq is the hub's sequence at the instant the
// subscriber registered, so every replayed event is at or below it and a live
// event published afterwards is still delivered. Using hub.Seq() after a plain
// Subscribe would allow a concurrent publish to over-report the cursor.
func TestSubscribeAfterReturnsAtomicCursor(t *testing.T) {
	hub := NewEventHub()
	for i := 1; i <= 3; i++ {
		hub.Publish(agent.Event{Kind: agent.EventNotice, Text: "old"})
	}

	ch, stop, seqAt := hub.SubscribeAfter(1)
	defer stop()
	if seqAt != 3 {
		t.Fatalf("subscribe cursor = %d, want 3 (the seq at registration)", seqAt)
	}

	// Everything replayed for after=1 is already covered by the cursor.
	for i := 0; i < 2; i++ {
		select {
		case <-ch:
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for replay %d", i)
		}
	}
	select {
	case ev := <-ch:
		t.Fatalf("replayed past the cursor: %+v", ev)
	default:
	}

	// A publish after registration gets a higher seq but must still reach the
	// subscriber; the handshake cursor intentionally stays behind it.
	hub.Publish(agent.Event{Kind: agent.EventNotice, Text: "live"})
	select {
	case ev := <-ch:
		if ev.Text != "live" {
			t.Fatalf("live event = %q, want live", ev.Text)
		}
	case <-time.After(time.Second):
		t.Fatal("live event not delivered")
	}
}

// publishOnHeaderStream publishes one event the first time the handler asks
// for the ResponseWriter's headers. The SSE handler calls Header() *after*
// subscribing but *before* it writes the server.connected frame, so this
// deterministically reproduces the race where a publish lands in that gap.
type publishOnHeaderStream struct {
	*streamRecorder
	hub  *EventHub
	text string
	once sync.Once
}

func (w *publishOnHeaderStream) Header() http.Header {
	w.once.Do(func() {
		w.hub.Publish(agent.Event{Kind: agent.EventNotice, Text: w.text})
	})
	return w.streamRecorder.Header()
}

// TestEventHandlerConnectedSeqIsSubscribeCursor guards the SSE handshake: the
// seq in server.connected must be the subscription-time cursor, not a later
// hub.Seq() reading. Otherwise a client that reconnects from that cursor skips
// any event published between subscribing and the handshake frame.
func TestEventHandlerConnectedSeqIsSubscribeCursor(t *testing.T) {
	hub := NewEventHub()
	rec := &publishOnHeaderStream{
		streamRecorder: newStreamRecorder(),
		hub:            hub,
		text:           "raced",
	}
	handler := newEventHandlerInterval(hub, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/v1/event", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(rec, req)
	}()

	rec.waitContains(t, "event: server.connected")
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("event handler did not stop on cancel")
	}

	body := rec.snapshot()
	if !strings.Contains(body, `"seq":0`) {
		t.Fatalf("connected cursor = %q, want seq 0 from subscription time (not a raced publish): %s", body, body)
	}
	// The event published in the gap is still delivered as a live frame.
	if !strings.Contains(body, `"Text":"raced"`) {
		t.Fatalf("event published during the handshake gap was lost: %s", body)
	}
}
