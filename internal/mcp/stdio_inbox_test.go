package mcp

import (
	"bytes"
	"sync"
	"testing"
)

// TestInboxBuffersBeforeBindAndReplaysInOrder 覆盖核心约定：
// 在处理器存在之前到达的消息不会被丢弃，而是按 FIFO 顺序
// 重放；bind 之后到达的消息走直接路径。
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

	// bind 之后处理器被内联调用，因此该值最后出现。
	in.buffer(4)
	if want := []int{1, 2, 3, 4}; !equalInts(got, want) {
		t.Fatalf("after direct delivery = %v, want %v", got, want)
	}
}

// TestInboxBindOnEmptyQueue 验证在队列为空时 bind 是空操作，且
// 仍为后续消息安装处理器。
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

// TestInboxDrainDoesNotReplay 验证被 drain 的消息不会被后续 bind
// 第二次投递（无重复投递）。
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

// TestInboxRebindDoesNotRedeliver 验证绑定第二个处理器
// 不会交出第一个处理器已消费的消息。
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

// collector 是并发测试使用的线程安全汇聚点；inbox 可能
// 同时从多个 goroutine 调用处理器。
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

// expectEach 断言 [0,n) 中的每个 id 都恰好被投递一次。
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

// TestInboxConcurrentBufferBeforeBind 证明当许多生产者在 bind 重放之前
// 填满队列时，没有消息丢失或重复。
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

// TestInboxConcurrentBufferAfterBind 证明一旦处理器被附加，
// 且消息来自许多 goroutine，直接投递是安全的。
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

// TestInboxConcurrentBindRacesWithBuffer 演练交接边界本身：
// bind 和生产者同时运行，因此每条消息可能走重放路径或直接路径，
// 但必须恰好走其中一条。
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

// TestInboxBytesMessages 检查不可比较的元素类型：原始字节帧
// 必须在缓冲和重放中保持不变。
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
