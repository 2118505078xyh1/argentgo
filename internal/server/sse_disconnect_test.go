package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/agent"
)

// disconnectRunner models a long-running upstream agent run: it blocks until
// its context is cancelled, then reports that cancellation. It lets the tests
// observe whether the SSE transport actually stops the run on client
// disconnect instead of leaving it consuming tokens in the background.
type disconnectRunner struct {
	started   chan struct{}
	cancelled chan struct{}
}

func (r *disconnectRunner) Prompt(ctx context.Context, _ string, _ func(string)) (string, error) {
	close(r.started)
	<-ctx.Done()
	close(r.cancelled)
	return "", ctx.Err()
}

func (r *disconnectRunner) Abort() {}

// TestPromptClientDisconnectCancelsUpstream verifies that /v1/prompt drives
// Runner.Prompt with a context derived from the request. When the client goes
// away (request context cancelled) the upstream run must be cancelled and the
// handler must return promptly, with no run left behind in the background.
func TestPromptClientDisconnectCancelsUpstream(t *testing.T) {
	runner := &disconnectRunner{
		started:   make(chan struct{}),
		cancelled: make(chan struct{}),
	}
	h := NewHandler(Config{Token: "t", Runner: runner})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req := httptest.NewRequest(http.MethodPost, "/v1/prompt", strings.NewReader(`{"text":"hi"}`)).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer t")
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(rec, req)
	}()

	select {
	case <-runner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("runner did not start")
	}

	// Simulate the client disconnecting while the run is in flight.
	cancel()

	select {
	case <-runner.cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream run was not cancelled on client disconnect")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("prompt handler did not return after client disconnect")
	}
}

// subscriberCount reports the number of live hub subscribers. It lives in a
// test file so the production EventHub API stays unchanged.
func (h *EventHub) subscriberCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subs)
}

// TestEventStreamUnsubscribesOnClientDisconnect verifies that the /v1/event
// SSE handler releases its hub subscription and its serving goroutine when the
// client disconnects. A leaked subscription would keep receiving published
// events and a leaked goroutine would keep the heartbeat ticker alive.
func TestEventStreamUnsubscribesOnClientDisconnect(t *testing.T) {
	hub := NewEventHub()
	handler := newEventHandlerInterval(hub, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	rec, done := startEventStream(t, handler, ctx)

	rec.waitContains(t, "event: server.connected")
	if got := hub.subscriberCount(); got != 1 {
		t.Fatalf("subscribers while streaming = %d, want 1", got)
	}

	if !stopEventStream(cancel, done) {
		t.Fatal("event handler did not stop on client disconnect")
	}
	if got := hub.subscriberCount(); got != 0 {
		t.Fatalf("subscribers after disconnect = %d, want 0 (subscription leak)", got)
	}

	// Publishing after the client left must not panic or block on a stale
	// subscriber; the hub should have no live subscribers at all.
	hub.Publish(agent.Event{Kind: agent.EventNotice, Text: "after-disconnect"})
	if got := hub.subscriberCount(); got != 0 {
		t.Fatalf("subscribers after publish = %d, want 0", got)
	}
}
