package server

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/agent"
)

// TestEventHubSubscribeLargeCursorNeverPanics guards the client-supplied replay
// cursor. after is a uint64 and may be as large as MaxUint64; it must be clamped
// in the uint64 domain before being converted to a slice index. Converting
// first (the old behaviour) wraps negative for after >= 2^63 and makes the
// replay loop index history with a negative offset, panicking the caller.
func TestEventHubSubscribeLargeCursorNeverPanics(t *testing.T) {
	tests := []struct {
		name    string
		after   uint64
		publish int
	}{
		{"empty history at 2^63", uint64(1) << 63, 0},
		{"empty history at max uint64", math.MaxUint64, 0},
		{"short history at 2^63", uint64(1) << 63, 3},
		{"short history at max int + 1", uint64(math.MaxInt64) + 1, 3},
		{"short history at max uint64", math.MaxUint64, 3},
		{"full history at max uint64", math.MaxUint64, eventHistoryCap},
		{"cursor just beyond seq", 4, 3},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hub := NewEventHub()
			for i := 0; i < tc.publish; i++ {
				hub.Publish(agent.Event{Kind: agent.EventNotice, Text: "old"})
			}

			// The cursor is past every published event: replay must be empty.
			// A panic here is the bug (the testing framework reports it as a
			// failure with a stack trace).
			ch, stop := hub.Subscribe(tc.after)
			defer stop()
			select {
			case ev := <-ch:
				t.Fatalf("unexpected replayed event for after=%d: %+v", tc.after, ev)
			case <-time.After(20 * time.Millisecond):
			}

			// The subscription is still live: a later publish must arrive.
			hub.Publish(agent.Event{Kind: agent.EventNotice, Text: "live"})
			select {
			case ev := <-ch:
				if ev.Text != "live" {
					t.Fatalf("live event = %q, want live", ev.Text)
				}
			case <-time.After(time.Second):
				t.Fatal("live event not delivered after large-cursor subscribe")
			}
		})
	}
}

// TestEventHubSubscribeCursorReplaysInRange makes sure clamping huge cursors
// does not regress normal in-range replay.
func TestEventHubSubscribeCursorReplaysInRange(t *testing.T) {
	hub := NewEventHub()
	for i := 1; i <= 3; i++ {
		hub.Publish(agent.Event{Kind: agent.EventNotice, Text: "e"})
	}
	for _, after := range []uint64{0, 1, 2, 3} {
		ch, stop := hub.Subscribe(after)
		want := 3 - int(after)
		got := 0
		for i := 0; i < want; i++ {
			select {
			case <-ch:
				got++
			case <-time.After(time.Second):
				stop()
				t.Fatalf("after=%d: timed out waiting for replay %d/%d", after, got, want)
			}
		}
		select {
		case <-ch:
			stop()
			t.Fatalf("after=%d: replayed more than %d events", after, want)
		default:
		}
		stop()
	}
}

// TestEventHandlerHugeAfterCursor exercises the same overflow through the SSE
// frontend (?after=...). A pre-cancelled context makes the handler return right
// after the connected frame, so no network or blocking is involved.
func TestEventHandlerHugeAfterCursor(t *testing.T) {
	tests := []string{
		"9223372036854775807",  // MaxInt64
		"9223372036854775808",  // 2^63, wraps int on 64-bit
		"18446744073709551615", // MaxUint64
	}
	for _, raw := range tests {
		t.Run(raw, func(t *testing.T) {
			hub := NewEventHub()
			for i := 0; i < 3; i++ {
				hub.Publish(agent.Event{Kind: agent.EventNotice, Text: "old"})
			}
			handler := newEventHandlerInterval(hub, time.Hour)
			ctx, cancel := context.WithCancel(context.Background())
			cancel() // exit the stream loop immediately after the connected frame

			req := httptest.NewRequest(http.MethodGet, "/v1/event?after="+raw, nil).WithContext(ctx)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req) // direct call: a panic fails this subtest

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			body := rec.Body.String()
			if !strings.Contains(body, "event: server.connected") {
				t.Fatalf("missing connected frame: %q", body)
			}
			if strings.Contains(body, "\"Text\":\"old\"") {
				t.Fatalf("cursor past seq replayed old events: %q", body)
			}
		})
	}
}
