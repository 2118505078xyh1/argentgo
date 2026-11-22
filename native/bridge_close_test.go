package main

import (
	"bufio"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestBridgeCloseHandlerFiresOnceOnEOF 用 io.Pipe 模拟 engine 连接断开：
// 正常响应帧不得触发断开回调；writer 关闭后 readLoop 遇到 EOF，
// onClose 恰好触发一次，并把 chat 状态切到含"断开"的断连态。
func TestBridgeCloseHandlerFiresOnceOnEOF(t *testing.T) {
	pr, pw := io.Pipe()
	b := &bridge{
		stdout:  bufio.NewReader(pr),
		pending: make(map[int]chan rpcReply),
	}
	app := &chat{status: "已连接引擎"}

	var calls atomic.Int32
	var closeErr error
	b.setCloseHandler(func(err error) {
		calls.Add(1)
		closeErr = err
		app.onEngineClose(err) // 测试无窗口，直接应用状态变更
	})

	// 注册一个待处理请求，确认正常帧已被 readLoop 消费。
	reply := make(chan rpcReply, 1)
	b.pending[1] = reply

	done := make(chan struct{})
	go func() {
		b.readLoop()
		close(done)
	}()

	if _, err := io.WriteString(pw, `{"jsonrpc":"2.0","id":1,"result":{}}`+"\n"); err != nil {
		t.Fatalf("write reply: %v", err)
	}
	select {
	case got := <-reply:
		if got.err != "" {
			t.Fatalf("reply error = %q, want none", got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("readLoop did not deliver the normal reply")
	}
	if calls.Load() != 0 || app.engineDisconnected {
		t.Fatalf("normal frame triggered close: calls=%d disconnected=%v", calls.Load(), app.engineDisconnected)
	}

	// writer 关闭 → reader EOF → readLoop 通知断开并返回。
	if err := pw.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("readLoop did not return after EOF")
	}

	if calls.Load() != 1 {
		t.Fatalf("onClose calls = %d, want exactly 1", calls.Load())
	}
	if closeErr == nil {
		t.Fatal("onClose received nil error, want the EOF/read error")
	}
	if !app.engineDisconnected {
		t.Fatal("engineDisconnected = false, want true")
	}
	if !strings.Contains(app.status, "断开") {
		t.Fatalf("status = %q, want it to contain 断开", app.status)
	}
}

// TestBridgeSetCloseHandlerGeneration 验证 close handler 的 generation
// 记账与"最新注册生效"语义，与 setDeltaHandler 保持一致。
func TestBridgeSetCloseHandlerGeneration(t *testing.T) {
	b := &bridge{pending: make(map[int]chan rpcReply)}
	first, second := 0, 0
	if gen := b.setCloseHandler(func(error) { first++ }); gen != 1 {
		t.Fatalf("first generation = %d, want 1", gen)
	}
	if gen := b.setCloseHandler(func(error) { second++ }); gen != 2 {
		t.Fatalf("second generation = %d, want 2", gen)
	}

	// strings.Reader 立即 EOF：readLoop 应把断开交给最新注册的回调。
	b.stdout = bufio.NewReader(strings.NewReader(""))
	b.readLoop()
	if first != 0 || second != 1 {
		t.Fatalf("first=%d second=%d; want only the latest handler called once", first, second)
	}
}

// TestBridgeCloseHandlerRegistrationNoRace 在 -race 下保持 close handler
// 安全：readLoop 在 mu 下快照回调，另一个 goroutine 并发交换它。
func TestBridgeCloseHandlerRegistrationNoRace(t *testing.T) {
	pr, pw := io.Pipe()
	b := &bridge{
		stdout:  bufio.NewReader(pr),
		pending: make(map[int]chan rpcReply),
	}
	done := make(chan struct{})
	go func() {
		b.readLoop()
		close(done)
	}()

	const rounds = 300
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			b.setCloseHandler(func(error) {})
		}
	}()
	wg.Wait()

	if err := pw.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("readLoop did not return after EOF")
	}
}
