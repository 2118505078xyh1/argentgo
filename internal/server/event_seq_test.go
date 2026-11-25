package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"example.com/argentgo/internal/agent"
)

// TestSubscribeAfterReturnsAtomicCursor 检查 SubscribeAfter 的续传游标约定：
// 返回的 seq 是订阅者注册那一刻 hub 的序列号，
// 因此每个回放事件都小于或等于它，而之后发布的实时
// 事件仍会被投递。在普通 Subscribe 之后再读 hub.Seq()
// 会让并发的发布把游标报得过大。
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

	// after=1 回放的所有事件都已被该游标覆盖。
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

	// 注册之后发布的事件会有更高的 seq，但仍必须到达
	// 订阅者；握手游标有意落后于它。
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

// publishOnHeaderStream 在处理器第一次请求 ResponseWriter 的头部时
// 发布一个事件。SSE 处理器在订阅*之后*、写 server.connected 帧*之前*
// 调用 Header()，因此这能确定性地复现发布落在那个间隙中的
// 竞态。
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

// TestEventHandlerConnectedSeqIsSubscribeCursor 守护 SSE 握手：
// server.connected 中的 seq 必须是订阅时刻的游标，而不是稍后
// 读取的 hub.Seq()。否则从该游标重连的客户端会跳过
// 订阅与握手帧之间发布的任何事件。
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
	// 等在握手间隙发布的事件也真正写入响应后再取消。若只等 connected 就
	// cancel，handler 可能在写出 raced 实时帧前就因 context 取消而提前退出，
	// 在慢机器上造成"事件丢失"的假失败。
	rec.waitContains(t, `"Text":"raced"`)
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
	// 在间隙中发布的事件仍作为实时帧送达。
	if !strings.Contains(body, `"Text":"raced"`) {
		t.Fatalf("event published during the handshake gap was lost: %s", body)
	}
}
