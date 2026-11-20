package mcp

import (
	"bytes"
	"sync"
	"testing"
)

// TestInboxBuffersBeforeBindAndReplaysInOrder covers the core contract:
// messages arriving before the handler exists are not dropped, replayed in
// FIFO order, and messages arriving after bind take the direct path.
func TestInboxBuffersBeforeBindAndReplaysInOrder(t *testing.T) {
	in := newInbox[int]()
	var got []int
	for _, v := range []int{1, 2, 3} {
		in.buffer(v)
	}

	in.bind(func(v int) { got = append(got, v) })

	if want := []int{1, 2, 3}; !equalInts(got, want) {
		t.Fatalf("replay = %v, want %v", got, want)
	}

	// After bind the handler is called inline, so this value appears last.
	in.buffer(4)
	if want := []int{1, 2, 3, 4}; !equalInts(got, want) {
		t.Fatalf("after direct delivery = %v, want %v", got, want)
	}
}

// TestInboxBindOnEmptyQueue verifies bind with nothing buffered is a no-op and
// still installs the handler for subsequent messages.
func TestInboxBindOnEmptyQueue(t *testing.T) {
	in := newInbox[string]()
	calls := 0
	in.bind(func(string) { calls++ })
	if calls != 0 {
		t.Fatalf("empty bind replayed %d messages", calls)
	}
	in.buffer("later")
	if calls != 1 {
		t.Fatalf("post-bind buffer delivered %d messages, want 1", calls)
	}
}

// TestInboxDrainDoesNotReplay verifies a drained message is never delivered a
// second time by a subsequent bind (no duplicate delivery).
func TestInboxDrainDoesNotReplay(t *testing.T) {
	in := newInbox[int]()
	in.buffer(10)
	in.buffer(20)
	drained := in.drain()
	if want := []int{10, 20}; !equalInts(drained, want) {
		t.Fatalf("drain = %v, want %v", drained, want)
	}
	if again := in.drain(); len(again) != 0 {
		t.Fatalf("second drain = %v, want empty", again)
	}

	replayed := 0
	in.bind(func(int) { replayed++ })
	if replayed != 0 {
		t.Fatalf("bind replayed %d already-drained messages", replayed)
	}
	in.buffer(30)
	if replayed != 1 {
		t.Fatalf("post-drain buffer delivered %d messages, want 1", replayed)
	}
}

// TestInboxRebindDoesNotRedeliver verifies that binding a second handler does
// not hand out messages the first handler already consumed.
func TestInboxRebindDoesNotRedeliver(t *testing.T) {
	in := newInbox[int]()
	in.buffer(7)
	first := 0
	in.bind(func(int) { first++ })

	second := 0
	in.bind(func(int) { second++ })
	if first != 1 || second != 0 {
		t.Fatalf("rebind: first=%d second=%d, want 1/0", first, second)
	}
	in.buffer(8)
	if first != 1 || second != 1 {
		t.Fatalf("after rebind buffer: first=%d second=%d, want 1/1", first, second)
	}
}

// collector is a thread-safe sink used by the concurrent tests; the inbox may
// invoke a handler from several goroutines at once.
type collector struct {
	mu sync.Mutex
	m  map[int]int
}

func newCollector() *collector { return &collector{m: make(map[int]int)} }

func (c *collector) add(v int) {
	c.mu.Lock()
	c.m[v]++
	c.mu.Unlock()
}

// expectEach asserts every id in [0,n) was delivered exactly once.
func (c *collector) expectEach(t *testing.T, n int) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) != n {
		t.Fatalf("received %d distinct messages, want %d: %v", len(c.m), n, c.m)
	}
	for i := 0; i < n; i++ {
		if c.m[i] != 1 {
			t.Fatalf("message %d delivered %d times, want exactly once", i, c.m[i])
		}
	}
}

// TestInboxConcurrentBufferBeforeBind proves no message is lost or duplicated
// when many producers fill the queue before bind replays it.
func TestInboxConcurrentBufferBeforeBind(t *testing.T) {
	const n = 200
	in := newInbox[int]()
	got := newCollector()

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(v int) {
			defer wg.Done()
			in.buffer(v)
		}(i)
	}
	wg.Wait()

	in.bind(got.add)
	got.expectEach(t, n)
}

// TestInboxConcurrentBufferAfterBind proves direct delivery is safe once the
// handler is attached and messages arrive from many goroutines.
func TestInboxConcurrentBufferAfterBind(t *testing.T) {
	const n = 200
	in := newInbox[int]()
	got := newCollector()
	in.bind(got.add)

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(v int) {
			defer wg.Done()
			in.buffer(v)
		}(i)
	}
	wg.Wait()

	got.expectEach(t, n)
}

// TestInboxConcurrentBindRacesWithBuffer exercises the handoff boundary itself:
// bind and producers run at the same time, so each message may travel the
// replay path or the direct path, but must travel exactly one of them.
func TestInboxConcurrentBindRacesWithBuffer(t *testing.T) {
	const n = 200
	in := newInbox[int]()
	got := newCollector()

	bound := make(chan struct{})
	go func() {
		in.bind(got.add)
		close(bound)
	}()

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(v int) {
			defer wg.Done()
			in.buffer(v)
		}(i)
	}
	wg.Wait()
	<-bound

	got.expectEach(t, n)
}

// TestInboxBytesMessages checks a non-comparable element type: raw byte frames
// must survive buffering and replay unchanged.
func TestInboxBytesMessages(t *testing.T) {
	in := newInbox[[]byte]()
	frames := [][]byte{[]byte(`{"id":1}`), {}, []byte(`{"id":2}`)}
	for _, frame := range frames {
		in.buffer(frame)
	}

	var got [][]byte
	in.bind(func(frame []byte) { got = append(got, frame) })

	if len(got) != len(frames) {
		t.Fatalf("replayed %d frames, want %d", len(got), len(frames))
	}
	for i := range frames {
		if !bytes.Equal(got[i], frames[i]) {
			t.Fatalf("frame %d = %q, want %q", i, got[i], frames[i])
		}
	}
	in.buffer([]byte("direct"))
	if len(got) != len(frames)+1 || !bytes.Equal(got[len(got)-1], []byte("direct")) {
		t.Fatalf("post-bind byte frame not delivered last: %q", got)
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
