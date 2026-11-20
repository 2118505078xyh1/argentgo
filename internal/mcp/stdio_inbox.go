package mcp

import "sync"

// inbox closes the window between "messages can arrive" and "the real handler
// is attached": calls to buffer made before bind are queued and replayed in
// order once bind installs the handler; calls made after bind are delivered
// immediately. It is the Go counterpart of omp's worker inbox (worker-host.ts),
// where a dynamically imported worker module attaches its message handler after
// the port has already flushed a synchronously posted init handshake.
//
// The type is deliberately independent of stdioTransport and of the wire
// protocol: it is a small, thread-safe primitive that can be reused by any
// producer/consumer pair with the same "early message" race. The zero value is
// ready to use; do not copy an inbox after first use.
type inbox[T any] struct {
	mu      sync.Mutex
	queued  []T
	ready   bool
	handler func(T)
}

// newInbox creates an empty inbox with no handler attached.
func newInbox[T any]() *inbox[T] { return &inbox[T]{} }

// buffer records msg. Before bind it only appends to the queue; afterwards it
// invokes the attached handler directly. It never blocks on the handler, so a
// handler that re-enters buffer cannot deadlock.
//
// The handler may be called from a different goroutine than buffer when bind
// replays the queue, so a handler shared across goroutines must be safe for
// concurrent use (or rely on the caller to serialize).
func (in *inbox[T]) buffer(msg T) {
	in.mu.Lock()
	if !in.ready || in.handler == nil {
		in.queued = append(in.queued, msg)
		in.mu.Unlock()
		return
	}
	h := in.handler
	in.mu.Unlock()
	h(msg)
}

// bind attaches handler and replays every message buffered so far, in FIFO
// order. The queue is detached under the lock while the handler is installed in
// the same critical section, so each buffered message is delivered exactly
// once, by either the replay or the direct path, never by both. A later bind
// replaces the handler but does not replay messages an earlier bind delivered.
func (in *inbox[T]) bind(handler func(T)) {
	in.mu.Lock()
	queued := in.queued
	in.queued = nil
	in.ready = true
	in.handler = handler
	in.mu.Unlock()

	for _, msg := range queued {
		handler(msg)
	}
}

// drain removes and returns all messages still waiting for a handler, in FIFO
// order. It is primarily useful for consumers that poll instead of binding; a
// message drained here is not replayed by a later bind, so no message is ever
// delivered twice.
func (in *inbox[T]) drain() []T {
	in.mu.Lock()
	queued := in.queued
	in.queued = nil
	in.mu.Unlock()
	return queued
}
