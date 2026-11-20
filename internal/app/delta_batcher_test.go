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

// TestDeltaBatcherMergesBurst proves a burst of text deltas becomes one Event
// with concatenated Text and inherited Turn/Depth metadata.
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

// TestDeltaBatcherFlushTail proves Flush emits the tail and is idempotent when
// nothing is pending.
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

// TestDeltaBatcherMaxDeltas proves the count limit flushes early and bounds a
// burst so it cannot grow without limit.
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

// TestDeltaBatcherMaxRunes proves the rune limit flushes on UI-safe boundaries
// and never splits a UTF-8 rune, including CJK and emoji.
func TestDeltaBatcherMaxRunes(t *testing.T) {
	// Each "界" and "🙂" is one rune but three/four bytes. A byte-based limit
	// would split them; a rune-based limit must not.
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
	// Concatenation must be lossless and rune-valid regardless of boundaries.
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

// TestDeltaBatcherCJKAndEmoji proves multi-byte text is preserved byte-for-byte
// through merging and flushing.
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

// TestDeltaBatcherEmptyInput proves empty text and non-delta events are ignored
// without perturbing the burst.
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

// TestDeltaBatcherPreservesOrder proves concatenation follows Add order even
// when other event kinds are interleaved between deltas.
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

// TestDeltaBatcherBoundaryFlushes proves a Turn/Depth change flushes the current
// burst first, so a merged event never spans two runs/agents.
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

// TestDeltaBatcherOversizedDeltaNotSplit proves a single delta larger than the
// rune limit is emitted intact instead of being truncated or split.
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

// TestDeltaBatcherReset proves Reset discards the pending tail without emitting.
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

// collectEmits drains events from a channel into a slice once the producer has
// signalled completion, avoiding races on the shared slice.
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

// TestRunDeltaBatchTimerFlushOnClose proves the timer driver merges a quick
// burst and releases the tail when its input channel closes.
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

// TestRunDeltaBatchTimerTicks proves a delta is flushed by the window tick
// (not only on close), which is the low-latency behaviour the driver provides.
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

// TestRunDeltaBatchTimerCancelFlushesTail proves cancellation releases the tail
// exactly once and unblocks the driver.
func TestRunDeltaBatchTimerCancelFlushesTail(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Unbuffered: the send completes only once the driver has received the
	// delta, guaranteeing it is folded into the batcher before we cancel.
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

// TestDefaultDeltaBatchWindow documents the deliberate low-latency window.
func TestDefaultDeltaBatchWindow(t *testing.T) {
	if DefaultDeltaBatchWindow <= 0 || DefaultDeltaBatchWindow > 50*time.Millisecond {
		t.Fatalf("DefaultDeltaBatchWindow = %v, want a low-latency window in (0, 50ms]", DefaultDeltaBatchWindow)
	}
	if !reflect.DeepEqual(DefaultDeltaBatchWindow, 32*time.Millisecond) {
		t.Fatalf("DefaultDeltaBatchWindow = %v, want 32ms", DefaultDeltaBatchWindow)
	}
}
