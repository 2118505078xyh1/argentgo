package dap

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

// newFakeClient 用 net.Pipe 造一个 Client：一端给 readLoop，另一端由测试扮演
// adapter，读 request 帧并回 response 帧。events 直接由测试注入。
func newFakeClient(t *testing.T) (*Client, net.Conn, func()) {
	t.Helper()
	clientSide, adapterSide := net.Pipe()
	// Client 的字段是包私有，同包测试可直接构造。
	c := &Client{
		pending: make(map[int64]chan response),
		events:  make(chan Event, 8),
		stdin:   clientSide,
		stdout:  clientSide,
		stderr:  clientSide,
		done:    make(chan struct{}),
	}
	go c.readLoop()
	return c, adapterSide, func() {
		_ = adapterSide.Close()
		_ = clientSide.Close()
	}
}

// adapterReply 扮演 adapter：读一帧 request，回一帧 success response。
func adapterReply(t *testing.T, conn net.Conn, wantCommand string) int {
	t.Helper()
	payload, err := readFrame(bufio.NewReader(conn))
	if err != nil {
		t.Fatalf("adapter read: %v", err)
	}
	var msg Message
	if err := json.Unmarshal(payload, &msg); err != nil {
		t.Fatalf("adapter decode: %v", err)
	}
	if msg.Command != wantCommand {
		t.Fatalf("adapter got command %q, want %q", msg.Command, wantCommand)
	}
	success := true
	resp := Message{Seq: 100, Type: TypeResponse, RequestSeq: msg.Seq, Success: &success, Command: msg.Command, Body: json.RawMessage(`{}`)}
	data, _ := json.Marshal(resp)
	if err := writeFrame(conn, data); err != nil {
		t.Fatalf("adapter write: %v", err)
	}
	return msg.Seq
}

func TestClientCallMatchesResponse(t *testing.T) {
	c, adapter, cleanup := newFakeClient(t)
	defer cleanup()

	done := make(chan struct{})
	go func() {
		adapterReply(t, adapter, "initialize")
		close(done)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	body, err := c.Call(ctx, "initialize", map[string]any{"adapterID": "test"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if body == nil {
		t.Fatal("response body should be non-nil json")
	}
	<-done
}

func TestClientEventDelivery(t *testing.T) {
	c, _, cleanup := newFakeClient(t)
	defer cleanup()
	// 直接注入一个 stopped 事件（adapter 推事件与 Call 无关）。
	c.events <- Event{Seq: 7, Event: "stopped", Body: json.RawMessage(`{"reason":"breakpoint","threadId":1}`)}
	select {
	case ev := <-c.Events():
		if ev.Event != "stopped" || ev.Seq != 7 {
			t.Fatalf("event = %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("event not delivered")
	}
}

func TestClientSeqIncreases(t *testing.T) {
	c, adapter, cleanup := newFakeClient(t)
	defer cleanup()
	go func() {
		adapterReply(t, adapter, "threads")
		adapterReply(t, adapter, "threads")
	}()
	ctx := context.Background()
	_, _ = c.Call(ctx, "threads", nil)
	_, _ = c.Call(ctx, "threads", nil)
	if got := c.seq.Load(); got != 2 {
		t.Fatalf("seq = %d, want 2", got)
	}
}

// 确认 Content-Length 帧里的 seq 是十进制整数（协议正确性）。
func TestFrameContainsDecimalSeq(t *testing.T) {
	var b strings.Builder
	seq := strconv.Itoa(42)
	b.WriteString("Content-Length: " + strconv.Itoa(len(seq)) + "\r\n\r\n" + seq)
	got, err := readFrame(bufio.NewReader(strings.NewReader(b.String())))
	if err != nil || string(got) != "42" {
		t.Fatalf("frame = %q, err = %v", got, err)
	}
}
