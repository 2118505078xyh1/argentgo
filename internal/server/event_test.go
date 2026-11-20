package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/agent"
)

func TestEventHubPublishSubscribeInOrder(t *testing.T) {
	hub := NewEventHub()
	ch, stop := hub.Subscribe(0)
	defer stop()

	for i, want := range []string{"a", "b", "c"} {
		if seq := hub.Publish(agent.Event{Kind: agent.EventNotice, Text: want}); seq != uint64(i+1) {
			t.Fatalf("publish %d seq = %d, want %d", i, seq, i+1)
		}
	}
	for _, want := range []string{"a", "b", "c"} {
		select {
		case ev := <-ch:
			if ev.Text != want {
				t.Fatalf("event text = %q, want %q", ev.Text, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %q", want)
		}
	}
	if got := hub.Seq(); got != 3 {
		t.Fatalf("seq = %d, want 3", got)
	}
}

func TestEventHubSubscribeReplaysAfterCursorThenFollowsLive(t *testing.T) {
	hub := NewEventHub()
	for i := 1; i <= 3; i++ {
		hub.Publish(agent.Event{Kind: agent.EventNotice, Text: fmt.Sprintf("e%d", i)})
	}

	ch, stop := hub.Subscribe(1)
	defer stop()
	for _, want := range []string{"e2", "e3"} {
		select {
		case ev := <-ch:
			if ev.Text != want {
				t.Fatalf("replayed event = %q, want %q", ev.Text, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for replay %q", want)
		}
	}

	hub.Publish(agent.Event{Kind: agent.EventNotice, Text: "live"})
	select {
	case ev := <-ch:
		if ev.Text != "live" {
			t.Fatalf("live event = %q, want live", ev.Text)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for live event")
	}
}

func TestEventHubEvictsSlowSubscriberOnly(t *testing.T) {
	hub := NewEventHub()
	slow, stopSlow := hub.Subscribe(0)
	defer stopSlow()
	fast, stopFast := hub.Subscribe(0)
	defer stopFast()

	// 每次发布后都让快速订阅者保持排空，使它不会被
	// 淘汰。慢速订阅者从不读取，因此恰好在第
	// eventSubCap+1 次发布时填满。
	for i := 0; i < eventSubCap; i++ {
		hub.Publish(agent.Event{Kind: agent.EventNotice, Text: "x"})
		select {
		case <-fast:
		default:
			t.Fatalf("fast subscriber missed event %d", i)
		}
	}
	hub.Publish(agent.Event{Kind: agent.EventNotice, Text: "overflow"})

	if got := hub.Evicted(); got != 1 {
		t.Fatalf("evicted = %d, want 1", got)
	}
	select {
	case ev := <-fast:
		if ev.Text != "overflow" {
			t.Fatalf("fast subscriber event = %q, want overflow", ev.Text)
		}
	default:
		t.Fatal("fast subscriber was evicted along with the slow one")
	}

	// 慢速订阅者的缓冲区仍会排空，然后干净地关闭。
	for i := 0; i < eventSubCap; i++ {
		select {
		case <-slow:
		case <-time.After(time.Second):
			t.Fatalf("timed out draining slow subscriber at %d", i)
		}
	}
	select {
	case _, ok := <-slow:
		if ok {
			t.Fatal("slow subscriber channel still open after eviction")
		}
	case <-time.After(time.Second):
		t.Fatal("slow subscriber channel was not closed after eviction")
	}
}

// TestEventHubDeliversTerminalWhenSubscriberBufferFull 是必达
// 约定：未排空普通配额的订阅者仍会收到
// 完成事件，而不是被淘汰并永远等待。
func TestEventHubDeliversTerminalWhenSubscriberBufferFull(t *testing.T) {
	hub := NewEventHub()
	ch, stop := hub.Subscribe(0)
	defer stop()

	for i := 0; i < eventSubCap; i++ {
		hub.Publish(agent.Event{Kind: agent.EventNotice, Text: "pending"})
	}
	hub.Publish(agent.Event{Kind: agent.EventStop, Reason: agent.StopComplete})

	if got := hub.Evicted(); got != 0 {
		t.Fatalf("evicted = %d, want 0: a terminal must not evict a subscriber", got)
	}
	for i := 0; i < eventSubCap; i++ {
		select {
		case <-ch:
		case <-time.After(time.Second):
			t.Fatalf("timed out draining progress event %d", i)
		}
	}
	select {
	case ev := <-ch:
		if !ev.IsTerminal() || ev.Reason != agent.StopComplete {
			t.Fatalf("delivered event = %+v, want stop/complete", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("terminal event was not delivered to the full subscriber")
	}
}

// TestEventHubKeepsTerminalsWhenReservedSlotFills 覆盖回退：一旦
// 预留槽位也被占用，hub 会牺牲一个旧的进度事件（仍在
// 回放日志中），而不是丢弃较新的终态事件或淘汰
// 订阅者。两个终态事件都保留并保持发布顺序。
func TestEventHubKeepsTerminalsWhenReservedSlotFills(t *testing.T) {
	hub := NewEventHub()
	ch, stop := hub.Subscribe(0)
	defer stop()

	for i := 0; i < eventSubCap; i++ {
		hub.Publish(agent.Event{Kind: agent.EventNotice, Text: "pending"})
	}
	hub.Publish(agent.Event{Kind: agent.EventStop, Reason: "first"})
	hub.Publish(agent.Event{Kind: agent.EventStop, Reason: "last"})

	if got := hub.Evicted(); got != 0 {
		t.Fatalf("evicted = %d, want 0", got)
	}
	stops, notices := 0, 0
	for i := 0; i < eventSubCap+1; i++ {
		select {
		case ev := <-ch:
			if ev.IsTerminal() {
				stops++
			} else {
				notices++
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out draining event %d", i)
		}
	}
	if stops != 2 || notices != eventSubCap-1 {
		t.Fatalf("got %d terminal(s) and %d progress event(s), want 2 and %d",
			stops, notices, eventSubCap-1)
	}
}

// TestEventHubStillEvictsForFullNonTerminalBuffer 守护已有的
// 背压行为：只有终态事件才走必达路径。
func TestEventHubStillEvictsForFullNonTerminalBuffer(t *testing.T) {
	hub := NewEventHub()
	ch, stop := hub.Subscribe(0)
	defer stop()

	for i := 0; i < eventSubCap; i++ {
		hub.Publish(agent.Event{Kind: agent.EventNotice, Text: "x"})
	}
	hub.Publish(agent.Event{Kind: agent.EventNotice, Text: "overflow"})

	if got := hub.Evicted(); got != 1 {
		t.Fatalf("evicted = %d, want 1 for a full non-terminal buffer", got)
	}
	for i := 0; i < eventSubCap; i++ {
		select {
		case <-ch:
		case <-time.After(time.Second):
			t.Fatalf("timed out draining event %d", i)
		}
	}
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("evicted subscriber channel still open")
		}
	case <-time.After(time.Second):
		t.Fatal("evicted subscriber channel was not closed")
	}
}

func TestEventHubUnsubscribeIsIdempotent(t *testing.T) {
	hub := NewEventHub()
	ch, stop := hub.Subscribe(0)
	stop()
	stop()

	hub.Publish(agent.Event{Kind: agent.EventNotice, Text: "ignored"})
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("received an event after unsubscribe")
		}
	case <-time.After(time.Second):
		t.Fatal("channel not closed after unsubscribe")
	}

	// 并发调用 stop 不得重复关闭通道。从
	// 当前游标订阅，使缓冲区中没有回放事件。
	ch2, stop2 := hub.Subscribe(hub.Seq())
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stop2()
		}()
	}
	wg.Wait()
	select {
	case _, ok := <-ch2:
		if ok {
			t.Fatal("channel still open after concurrent unsubscribe")
		}
	case <-time.After(time.Second):
		t.Fatal("channel not closed after concurrent unsubscribe")
	}
}

func TestEventHubHistoryIsBounded(t *testing.T) {
	hub := NewEventHub()
	total := eventHistoryCap + 50
	for i := 0; i < total; i++ {
		hub.Publish(agent.Event{Kind: agent.EventNotice, Text: fmt.Sprintf("e%d", i)})
	}

	ch, stop := hub.Subscribe(0)
	defer stop()
	for i := total - eventHistoryCap; i < total; i++ {
		want := fmt.Sprintf("e%d", i)
		select {
		case ev := <-ch:
			if ev.Text != want {
				t.Fatalf("replayed event = %q, want %q", ev.Text, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for replay %q", want)
		}
	}
	select {
	case ev := <-ch:
		t.Fatalf("replayed more than %d events: extra %q", eventHistoryCap, ev.Text)
	case <-time.After(20 * time.Millisecond):
	}
}

// streamRecorder 是带 flush 信号、受互斥锁保护的 http.ResponseWriter，
// 因此无需真实 socket 且无数据竞争即可断言 SSE 输出。
type streamRecorder struct {
	mu      sync.Mutex
	header  http.Header
	status  int
	body    bytes.Buffer
	flushed chan struct{}
}

func newStreamRecorder() *streamRecorder {
	return &streamRecorder{header: make(http.Header), flushed: make(chan struct{}, 32)}
}

func (s *streamRecorder) Header() http.Header { return s.header }

func (s *streamRecorder) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.body.Write(p)
}

func (s *streamRecorder) WriteHeader(code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status == 0 {
		s.status = code
	}
}

func (s *streamRecorder) Flush() {
	s.mu.Lock()
	if s.status == 0 {
		s.status = http.StatusOK
	}
	s.mu.Unlock()
	select {
	case s.flushed <- struct{}{}:
	default:
	}
}

func (s *streamRecorder) snapshot() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.body.String()
}

func (s *streamRecorder) waitContains(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if strings.Contains(s.snapshot(), want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %q; body = %q", want, s.snapshot())
		}
		select {
		case <-s.flushed:
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func startEventStream(t *testing.T, handler http.Handler, ctx context.Context) (*streamRecorder, <-chan struct{}) {
	t.Helper()
	rec := newStreamRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/event", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(rec, req)
	}()
	return rec, done
}

func stopEventStream(cancel context.CancelFunc, done <-chan struct{}) bool {
	cancel()
	select {
	case <-done:
		return true
	case <-time.After(2 * time.Second):
		return false
	}
}

func TestEventHandlerSendsConnectedThenEvents(t *testing.T) {
	hub := NewEventHub()
	handler := newEventHandlerInterval(hub, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	rec, done := startEventStream(t, handler, ctx)
	defer func() {
		if !stopEventStream(cancel, done) {
			t.Error("event handler did not stop on cancel")
		}
	}()

	rec.waitContains(t, "event: server.connected\ndata: {\"seq\":0}\n\n")
	hub.Publish(agent.Event{Kind: agent.EventNotice, Text: "hello"})
	rec.waitContains(t, "event: notice\ndata: {\"Kind\":\"notice\"")
	if !strings.Contains(rec.snapshot(), "\"Text\":\"hello\"") {
		t.Fatalf("event data missing text: %q", rec.snapshot())
	}
	if strings.Contains(rec.snapshot(), ": heartbeat") {
		t.Fatal("unexpected heartbeat with a one-hour interval")
	}

	if !stopEventStream(cancel, done) {
		t.Fatal("event handler did not stop on cancel")
	}
	// 在处理器退出后读取头部，避免与其写入竞争。
	if got := rec.header.Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := rec.header.Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q", got)
	}
	if got := rec.header.Get("X-Accel-Buffering"); got != "no" {
		t.Errorf("X-Accel-Buffering = %q", got)
	}
}

func TestEventHandlerHeartbeat(t *testing.T) {
	hub := NewEventHub()
	handler := newEventHandlerInterval(hub, 20*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	rec, done := startEventStream(t, handler, ctx)
	defer func() {
		if !stopEventStream(cancel, done) {
			t.Error("event handler did not stop on cancel")
		}
	}()

	rec.waitContains(t, "event: server.connected")
	rec.waitContains(t, ": heartbeat\n\n")
}

func TestEventHubConcurrentPublishSubscribe(t *testing.T) {
	hub := NewEventHub()
	var wg sync.WaitGroup
	for p := 0; p < 4; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				hub.Publish(agent.Event{Kind: agent.EventNotice, Text: fmt.Sprintf("%d:%d", p, i)})
			}
		}(p)
	}
	for s := 0; s < 4; s++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch, stop := hub.Subscribe(0)
			defer stop()
			for {
				select {
				case _, ok := <-ch:
					if !ok {
						return
					}
				case <-time.After(20 * time.Millisecond):
					return
				}
			}
		}()
	}
	wg.Wait()
	if got := hub.Seq(); got != 400 {
		t.Fatalf("seq = %d, want 400", got)
	}
}

func TestWireAgentEventsForwardsThenStops(t *testing.T) {
	hub := NewEventHub()
	ch, stop := hub.Subscribe(0)
	defer stop()

	emit := make(chan agent.Event, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := WireAgentEvents(hub, emit, ctx)

	emit <- agent.Event{Kind: agent.EventNotice, Text: "first"}
	select {
	case ev := <-ch:
		if ev.Text != "first" {
			t.Fatalf("forwarded event = %q, want first", ev.Text)
		}
	case <-time.After(time.Second):
		t.Fatal("bridge did not forward the event")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("bridge did not stop on context cancellation")
	}
	emit <- agent.Event{Kind: agent.EventNotice, Text: "after-cancel"}
	select {
	case ev := <-ch:
		t.Fatalf("bridge forwarded %q after cancellation", ev.Text)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestEventRouteRequiresAuthAndStreams(t *testing.T) {
	hub := NewEventHub()
	h := NewHandler(Config{Token: "t", Runner: &fakeRunner{}, Events: hub})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/event", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d, want 401", rec.Code)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled so the handler writes its first frame then exits
	req := httptest.NewRequest(http.MethodGet, "/v1/event", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer t")
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusOK {
		t.Fatalf("authorized status = %d, want 200", rec2.Code)
	}
	if !strings.Contains(rec2.Body.String(), "event: server.connected") {
		t.Fatalf("missing connected frame: %q", rec2.Body.String())
	}
}

func readJSONSoon(t *testing.T, dec *json.Decoder) map[string]any {
	t.Helper()
	type result struct {
		msg map[string]any
		err error
	}
	ch := make(chan result, 1)
	go func() {
		var msg map[string]any
		err := dec.Decode(&msg)
		ch <- result{msg: msg, err: err}
	}()
	select {
	case res := <-ch:
		if res.err != nil {
			t.Fatalf("decode message: %v", res.err)
		}
		return res.msg
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for JSON-RPC message")
		return nil
	}
}

func TestRPCSubscribeStreamsEventNotifications(t *testing.T) {
	hub := NewEventHub()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- RunRPC(ctx, inR, outW, Config{Runner: &fakeRunner{}, Events: hub})
	}()

	enc := json.NewEncoder(inW)
	dec := json.NewDecoder(outR)
	if err := enc.Encode(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "event/subscribe",
		"params": map[string]any{"after": 0},
	}); err != nil {
		t.Fatalf("encode subscribe: %v", err)
	}
	result := readJSONSoon(t, dec)
	if result["id"] != float64(1) {
		t.Fatalf("subscribe result = %v", result)
	}
	body, _ := result["result"].(map[string]any)
	if body["seq"] != float64(0) {
		t.Fatalf("subscribe seq = %v, want 0", body["seq"])
	}

	hub.Publish(agent.Event{Kind: agent.EventNotice, Text: "wake"})
	notification := readJSONSoon(t, dec)
	if notification["method"] != "event" {
		t.Fatalf("notification = %v, want method event", notification)
	}
	params, _ := notification["params"].(map[string]any)
	if params["kind"] != string(agent.EventNotice) {
		t.Fatalf("notification kind = %v", params["kind"])
	}
	eventBody, _ := params["event"].(map[string]any)
	if eventBody["Text"] != "wake" {
		t.Fatalf("notification event = %v", params["event"])
	}

	cancel()
	_ = inW.Close()
	waitFor(t, done)
}
