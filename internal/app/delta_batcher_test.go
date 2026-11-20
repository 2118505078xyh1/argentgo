package app

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/agent"
)

func delta(turn, depth int, text string) agent.Event {
	return agent.Event{Kind: agent.EventTextDelta, Turn: turn, Depth: depth, Text: text}
}

// TestDeltaBatcherMergesBurst 证明一串文本增量会合并为一个事件，
// 其 Text 是拼接结果，并继承 Turn/Depth 元数据。
func TestDeltaBatcherMergesBurst(t *testing.T) {
	b := newDeltaBatcher(16, 1024)
	for _, e := range []agent.Event{
		delta(3, 1, "Hel"),
		delta(3, 1, "lo, "),
		delta(3, 1, "world"),
	} {
		if out, emit := b.Add(e); emit {
			t.Fatalf("unexpected early emit while merging burst: %+v", out)
		}
	}
	out, ok := b.Flush()
	if !ok {
		t.Fatal("Flush must release a pending burst")
	}
	want := delta(3, 1, "Hello, world")
	if out != want {
		t.Fatalf("merged event = %+v, want %+v", out, want)
	}
	if b.Pending() {
		t.Fatal("batcher must be empty after Flush")
	}
}

// TestDeltaBatcherFlushTail 证明 Flush 会发出尾部，且在没有待处理内容时
// 是幂等的。
func TestDeltaBatcherFlushTail(t *testing.T) {
	b := newDeltaBatcher(0, 0)
	b.Add(delta(0, 0, "tail-"))
	b.Add(delta(0, 0, "chunk"))
	out, ok := b.Flush()
	if !ok || out.Text != "tail-chunk" {
		t.Fatalf("Flush tail = %q, ok=%v; want %q", out.Text, ok, "tail-chunk")
	}
	if out2, ok2 := b.Flush(); ok2 || out2 != (agent.Event{}) {
		t.Fatalf("second Flush = %+v, ok=%v; want zero, false", out2, ok2)
	}
}

// TestDeltaBatcherMaxDeltas 证明数量上限会提前刷新，并限制
// 一串增量使其不会无限增长。
func TestDeltaBatcherMaxDeltas(t *testing.T) {
	b := newDeltaBatcher(3, 0)
	var emitted []agent.Event
	for i := 0; i < 7; i++ {
		if out, emit := b.Add(delta(0, 0, "x")); emit {
			emitted = append(emitted, out)
		}
	}
	if len(emitted) != 2 {
		t.Fatalf("emitted %d bursts with maxDeltas=3 over 7 deltas, want 2", len(emitted))
	}
	for i, out := range emitted {
		if out.Text != "xxx" {
			t.Fatalf("burst %d text = %q, want %q", i, out.Text, "xxx")
		}
	}
	tail, ok := b.Flush()
	if !ok || tail.Text != "x" {
		t.Fatalf("tail = %q, ok=%v; want one remaining delta", tail.Text, ok)
	}
}

// TestDeltaBatcherMaxRunes 证明 rune 上限会在 UI 安全的边界处刷新，
// 且从不切断 UTF-8 rune，包括 CJK 和 emoji。
func TestDeltaBatcherMaxRunes(t *testing.T) {
	// 每个 "界" 和 "🙂" 都是一个 rune 但占三/四个字节。按字节的上限
	// 会切断它们；按 rune 的上限则不会。
	b := newDeltaBatcher(0, 4)
	var emitted []string
	feed := []string{"你", "好", "世", "界", "🙂", "A", "B"}
	for _, s := range feed {
		if out, emit := b.Add(delta(0, 0, s)); emit {
			emitted = append(emitted, out.Text)
		}
	}
	if tail, ok := b.Flush(); ok {
		emitted = append(emitted, tail.Text)
	}
	// 无论边界如何，拼接都必须无损且 rune 合法。
	if got, want := strings.Join(emitted, ""), strings.Join(feed, ""); got != want {
		t.Fatalf("lossless concat = %q, want %q", got, want)
	}
	for i, s := range emitted {
		if !utf8.ValidString(s) {
			t.Fatalf("burst %d is invalid UTF-8: %q", i, s)
		}
		if utf8.RuneCountInString(s) > 4 {
			t.Fatalf("burst %d has %d runes, exceeds limit 4: %q", i, utf8.RuneCountInString(s), s)
		}
	}
}

// TestDeltaBatcherCJKAndEmoji 证明多字节文本在合并和刷新过程中
// 逐字节保持不变。
func TestDeltaBatcherCJKAndEmoji(t *testing.T) {
	b := newDeltaBatcher(16, 1024)
	parts := []string{"中文", "🚀", "🎉 mixed", "テスト", "한국어"}
	var want strings.Builder
	for _, p := range parts {
		b.Add(delta(0, 0, p))
		want.WriteString(p)
	}
	out, ok := b.Flush()
	if !ok || out.Text != want.String() {
		t.Fatalf("CJK/emoji merge = %q, ok=%v; want %q", out.Text, ok, want.String())
	}
	if utf8.RuneCountInString(out.Text) != utf8.RuneCountInString(want.String()) {
		t.Fatal("rune count changed across merge")
	}
}

// TestDeltaBatcherEmptyInput 证明空文本和非增量事件会被忽略，
// 且不会扰乱当前串。
func TestDeltaBatcherEmptyInput(t *testing.T) {
	b := newDeltaBatcher(0, 0)
	ignored := []agent.Event{
		{Text: ""},
		{Kind: agent.EventTextDelta, Text: ""},
		{Kind: agent.EventToolStart, Text: "noise"},
		{Kind: agent.EventToolEnd, Text: "noise"},
		{Kind: agent.EventStop, Text: "noise"},
	}
	for _, e := range ignored {
		if out, emit := b.Add(e); emit || b.Pending() {
			t.Fatalf("Add(%+v) affected the batcher: emit=%v pending=%v out=%+v", e, emit, b.Pending(), out)
		}
	}
	if out, ok := b.Flush(); ok || out != (agent.Event{}) {
		t.Fatalf("Flush after ignored input = %+v, ok=%v; want zero, false", out, ok)
	}
}

// TestDeltaBatcherPreservesOrder 证明即使其他种类的事件夹杂在增量之间，
// 拼接也遵循 Add 的顺序。
func TestDeltaBatcherPreservesOrder(t *testing.T) {
	b := newDeltaBatcher(32, 4096)
	b.Add(delta(0, 0, "a"))
	b.Add(agent.Event{Kind: agent.EventToolStart, ToolName: "interleaved"})
	b.Add(delta(0, 0, "b"))
	b.Add(agent.Event{Kind: agent.EventError, Err: "interleaved"})
	b.Add(delta(0, 0, "c"))
	out, ok := b.Flush()
	if !ok || out.Text != "abc" {
		t.Fatalf("order = %q, ok=%v; want %q", out.Text, ok, "abc")
	}
}

// TestDeltaBatcherBoundaryFlushes 证明 Turn/Depth 改变会先刷新当前串，
// 使合并事件绝不会跨越两次运行/agent。
func TestDeltaBatcherBoundaryFlushes(t *testing.T) {
	b := newDeltaBatcher(32, 4096)
	b.Add(delta(1, 0, "one"))
	b.Add(delta(1, 0, "-two"))
	out, emit := b.Add(delta(2, 0, "three"))
	if !emit || out.Text != "one-two" {
		t.Fatalf("boundary emit = %q, %v; want %q, true", out.Text, emit, "one-two")
	}
	if out.Turn != 1 {
		t.Fatalf("flushed burst Turn = %d, want 1", out.Turn)
	}
	tail, ok := b.Flush()
	if !ok || tail.Text != "three" || tail.Turn != 2 {
		t.Fatalf("tail = %+v, ok=%v; want Turn=2 text=three", tail, ok)
	}
}

// TestDeltaBatcherOversizedDeltaNotSplit 证明单个大于 rune 上限的增量
// 会被完整发出，而非被截断或拆分。
func TestDeltaBatcherOversizedDeltaNotSplit(t *testing.T) {
	b := newDeltaBatcher(0, 2)
	big := strings.Repeat("🙂", 5)
	b.Add(delta(0, 0, big))
	out, ok := b.Flush()
	if !ok || out.Text != big {
		t.Fatalf("oversized delta = %q, ok=%v; want intact %q", out.Text, ok, big)
	}
	if utf8.RuneCountInString(out.Text) != 5 {
		t.Fatalf("oversized delta runes = %d, want 5", utf8.RuneCountInString(out.Text))
	}
}

// TestDeltaBatcherReset 证明 Reset 会丢弃待处理的尾部而不发出它。
func TestDeltaBatcherReset(t *testing.T) {
	b := newDeltaBatcher(0, 0)
	b.Add(delta(0, 0, "discard"))
	b.Reset()
	if b.Pending() {
		t.Fatal("batcher still pending after Reset")
	}
	if out, ok := b.Flush(); ok || out != (agent.Event{}) {
		t.Fatalf("Flush after Reset = %+v, ok=%v; want zero, false", out, ok)
	}
}

// collectEmits 在生产方发出完成信号后，把通道中的事件抽取到切片中，
// 避免对共享切片产生竞态。
type emitCollector struct {
	mu     sync.Mutex
	events []agent.Event
}

func (c *emitCollector) emit(e agent.Event) {
	c.mu.Lock()
	c.events = append(c.events, e)
	c.mu.Unlock()
}

func (c *emitCollector) snapshot() []agent.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]agent.Event(nil), c.events...)
}

// TestRunDeltaBatchTimerFlushOnClose 证明定时器驱动器会合并快速到达的
// 一串增量，并在输入通道关闭时释放尾部。
func TestRunDeltaBatchTimerFlushOnClose(t *testing.T) {
	in := make(chan agent.Event)
	var c emitCollector
	done := make(chan struct{})
	go func() {
		runDeltaBatchTimer(context.Background(), in, time.Second, c.emit)
		close(done)
	}()

	for _, p := range []string{"a", "b", "c"} {
		in <- delta(0, 0, p)
	}
	close(in)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timer driver did not stop after input closed")
	}

	got := c.snapshot()
	if len(got) != 1 || got[0].Text != "abc" {
		t.Fatalf("emitted = %+v; want one merged event with text %q", got, "abc")
	}
}

// TestRunDeltaBatchTimerTicks 证明增量会被窗口计时刷新
// （不只在关闭时），这正是驱动器提供的低延迟行为。
func TestRunDeltaBatchTimerTicks(t *testing.T) {
	in := make(chan agent.Event)
	emitted := make(chan agent.Event, 4)
	done := make(chan struct{})
	go func() {
		runDeltaBatchTimer(context.Background(), in, 15*time.Millisecond, func(e agent.Event) {
			emitted <- e
		})
		close(done)
	}()

	in <- delta(0, 0, "first")
	select {
	case got := <-emitted:
		if got.Text != "first" {
			t.Fatalf("tick emitted %q, want %q", got.Text, "first")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timer tick did not flush the pending delta")
	}

	in <- delta(0, 0, "second")
	close(in)
	select {
	case got := <-emitted:
		if got.Text != "second" {
			t.Fatalf("tail emitted %q, want %q", got.Text, "second")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("closing input did not flush the tail")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timer driver did not stop")
	}
}

// TestRunDeltaBatchTimerCancelFlushesTail 证明取消会恰好释放一次尾部，
// 并解除驱动器的阻塞。
func TestRunDeltaBatchTimerCancelFlushesTail(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// 无缓冲：只有在驱动器接收到增量之后，发送才完成，
	// 从而保证在我们取消之前，它已被折叠进批处理器。
	in := make(chan agent.Event)
	var c emitCollector
	done := make(chan struct{})
	go func() {
		runDeltaBatchTimer(ctx, in, time.Second, c.emit)
		close(done)
	}()

	sent := make(chan struct{})
	go func() {
		in <- delta(0, 0, "cancel-tail")
		close(sent)
	}()
	select {
	case <-sent:
	case <-time.After(2 * time.Second):
		t.Fatal("driver did not receive the delta")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timer driver did not stop on cancel")
	}
	got := c.snapshot()
	if len(got) != 1 || got[0].Text != "cancel-tail" {
		t.Fatalf("cancel flush = %+v; want one event with text %q", got, "cancel-tail")
	}
}

// TestDefaultDeltaBatchWindow 记录这个刻意设置的低延迟窗口。
func TestDefaultDeltaBatchWindow(t *testing.T) {
	if DefaultDeltaBatchWindow <= 0 || DefaultDeltaBatchWindow > 50*time.Millisecond {
		t.Fatalf("DefaultDeltaBatchWindow = %v, want a low-latency window in (0, 50ms]", DefaultDeltaBatchWindow)
	}
	if !reflect.DeepEqual(DefaultDeltaBatchWindow, 32*time.Millisecond) {
		t.Fatalf("DefaultDeltaBatchWindow = %v, want 32ms", DefaultDeltaBatchWindow)
	}
}
