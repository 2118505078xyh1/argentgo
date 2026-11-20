package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/agent"
)

const (
	// eventHistoryCap bounds the replay log. After a cursor, at most this many
	// most-recent events are replayed before the live stream begins.
	eventHistoryCap = 256
	// eventSubCap is the per-subscriber normal buffer quota. It is >=
	// eventHistoryCap so a replaying subscriber never blocks while the hub
	// holds its write lock, and has headroom before the first live event can
	// arrive.
	eventSubCap = 512
	// eventTerminalSlots is capacity reserved beyond eventSubCap for
	// must-deliver terminal events (EventStop). A subscriber is evicted once
	// its normal quota is full, so the reserved slot lets a final terminal
	// reach a slow subscriber instead of evicting it and leaving a headless
	// client waiting forever for the completion event.
	eventTerminalSlots = 1
	// eventHeartbeatInterval keeps idle SSE connections alive through proxies.
	eventHeartbeatInterval = 15 * time.Second
)

// EventHub is an in-order, event-sourced broadcaster with a bounded replay log.
//
// Publish assigns every event a monotonically increasing sequence number and
// delivers it to each subscriber in that same order. Subscribe first replays
// the tail of the history after a cursor, then follows live events. Subscriber
// channels are buffered; a subscriber whose normal buffer quota fills is
// evicted (its channel is closed and it is removed) instead of stalling the
// publisher or the other subscribers. Terminal events (EventStop) are the
// exception: they are must-deliver and use a reserved slot rather than evicting
// a full subscriber.
//
// The hub is safe for concurrent use by multiple publishers and subscribers.
// It is deliberately transport-agnostic: both the HTTP/SSE and --rpc frontends
// can share one instance via Config.Events.
type EventHub struct {
	mu      sync.RWMutex
	seq     uint64
	subs    map[chan agent.Event]struct{}
	history []agent.Event
	evicted uint64
}

// NewEventHub returns an empty hub ready for use.
func NewEventHub() *EventHub {
	return &EventHub{
		subs:    make(map[chan agent.Event]struct{}),
		history: make([]agent.Event, 0, eventHistoryCap),
	}
}

// Seq returns the sequence number of the most recently published event. It is
// zero before the first Publish.
func (h *EventHub) Seq() uint64 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.seq
}

// Evicted returns the number of slow subscribers that have been evicted so far.
// It is primarily useful for observability and tests.
func (h *EventHub) Evicted() uint64 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.evicted
}

// Publish appends e to the bounded replay log, assigns it the next sequence
// number, and broadcasts it to all current subscribers. It returns the assigned
// sequence.
//
// Delivery is non-blocking. A subscriber whose normal buffer quota is full is
// evicted (its channel is closed) and counted, except for must-deliver terminal
// events, which still reach it. This never delays the remaining subscribers.
func (h *EventHub) Publish(e agent.Event) uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.seq++
	if len(h.history) == eventHistoryCap {
		// Sliding window: drop the oldest event to keep the log bounded.
		copy(h.history, h.history[1:])
		h.history[len(h.history)-1] = e
	} else {
		h.history = append(h.history, e)
	}

	for ch := range h.subs {
		if e.IsTerminal() {
			// Must-deliver: never evict a subscriber for a terminal event, or
			// a headless client that has not drained its progress events would
			// miss the completion signal. The reserved slot is free whenever
			// the subscriber still holds only its normal quota; if even that
			// is taken, make room by discarding the oldest buffered progress
			// event (still recoverable from the replay log) rather than losing
			// the terminal.
			select {
			case ch <- e:
			default:
				<-ch
				ch <- e
			}
			continue
		}
		if len(ch) >= eventSubCap {
			// Slow consumer: close it and carry on. Deleting while ranging is
			// safe in Go, and the map membership check in stop() prevents a
			// double close if the subscriber unsubscribes at the same time.
			close(ch)
			delete(h.subs, ch)
			h.evicted++
			continue
		}
		ch <- e // cap > eventSubCap, so a sub below the quota cannot block.
	}
	return h.seq
}

// Subscribe registers a new subscriber. History events with a sequence greater
// than after are replayed first (the most recent tail, bounded by
// eventHistoryCap), then live events follow in publish order.
//
// The returned stop function is idempotent: it unsubscribes and closes the
// channel exactly once, even if the subscriber was already evicted, and can be
// called concurrently with Publish or with itself. When a subscriber is
// evicted its channel closes cleanly after the buffered events are drained.
func (h *EventHub) Subscribe(after uint64) (<-chan agent.Event, func()) {
	ch := make(chan agent.Event, eventSubCap+eventTerminalSlots)

	h.mu.Lock()
	if h.subs == nil {
		h.subs = make(map[chan agent.Event]struct{})
	}
	h.subs[ch] = struct{}{}

	// The oldest retained event has sequence h.seq - len(history) + 1.
	// Compute that base and replay only events newer than the cursor. Registration
	// and replay share one lock, so no live event can slip in between them and
	// break ordering. The replay count is <= eventHistoryCap < eventSubCap, so
	// these sends cannot block.
	base := h.seq - uint64(len(h.history)) + 1
	start := 0
	if after >= base {
		// Number of retained events at or before the cursor. Clamp in the
		// uint64 domain *before* converting to int: after is client-supplied
		// and may be as large as MaxUint64, and int(after-base+1) would wrap
		// negative, making the replay loop index history with a negative
		// offset and panic the caller (the whole process on --rpc).
		skip := after - base + 1
		if skip >= uint64(len(h.history)) {
			start = len(h.history)
		} else {
			start = int(skip)
		}
	}
	for i := start; i < len(h.history); i++ {
		ch <- h.history[i]
	}
	h.mu.Unlock()

	var once sync.Once
	stop := func() {
		once.Do(func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			if _, ok := h.subs[ch]; ok {
				close(ch)
				delete(h.subs, ch)
			}
		})
	}
	return ch, stop
}

// NewEventHandler exposes the hub as a Server-Sent Events stream.
//
// The first frame is always `event: server.connected` with `{"seq":N}` in its
// data, so a client knows the current cursor and can reconnect with ?after=N.
// Each subsequent frame is `event: <kind>` with the agent.Event JSON as data.
// An idle stream emits a `: heartbeat` comment every 15 seconds, and the
// request context's cancellation unsubscribes the client on disconnect.
func NewEventHandler(hub *EventHub) http.Handler {
	return newEventHandlerInterval(hub, eventHeartbeatInterval)
}

// newEventHandlerInterval is a test seam for the heartbeat interval.
func newEventHandlerInterval(hub *EventHub, interval time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodNotAllowed(w, http.MethodGet)
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			writeError(w, http.StatusInternalServerError, "streaming unsupported")
			return
		}

		var after uint64
		if raw := r.URL.Query().Get("after"); raw != "" {
			if n, err := strconv.ParseUint(raw, 10, 64); err == nil {
				after = n
			}
		}

		ch, stop := hub.Subscribe(after)
		defer stop()

		header := w.Header()
		header.Set("Content-Type", "text/event-stream")
		header.Set("Cache-Control", "no-cache")
		header.Set("Connection", "keep-alive")
		header.Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)

		if _, err := fmt.Fprintf(w, "event: server.connected\ndata: {\"seq\":%d}\n\n", hub.Seq()); err != nil {
			return
		}
		flusher.Flush()

		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
					return
				}
				flusher.Flush()
			case ev, ok := <-ch:
				if !ok {
					return // evicted: channel drained and closed
				}
				data, err := json.Marshal(ev)
				if err != nil {
					continue
				}
				if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Kind, data); err != nil {
					return
				}
				flusher.Flush()
			}
		}
	})
}

// WireAgentEvents bridges an agent event channel into the hub in a background
// goroutine. It stops when ctx is cancelled or emit is closed. The returned
// channel closes when the goroutine exits so callers can wait for the bridge to
// stop; the return value may be ignored.
//
// Wiring note for internal/app (the only package that imports everything):
//
//	hub := server.NewEventHub()
//	server.WireAgentEvents(hub, agentEvents, ctx)
//	cfg.Events = hub // shared by --serve (/v1/event) and --rpc (event/subscribe)
func WireAgentEvents(hub *EventHub, emit <-chan agent.Event, ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-emit:
				if !ok {
					return
				}
				hub.Publish(ev)
			}
		}
	}()
	return done
}
