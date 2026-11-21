package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/agent"
)

// panicRunner 在 Prompt 中 panic，用于验证传输层的 fire-and-forget goroutine
// 能兜底恢复而不终结整个进程。panicFirstOnly 为 true 时只有第一次调用 panic，
// 便于验证 ACP 在 panic 后释放 busy 槽位、后续提示仍可正常进行。
type panicRunner struct {
	mu             sync.Mutex
	calls          int
	panicFirstOnly bool
}

func (r *panicRunner) Prompt(_ context.Context, text string, onText func(string)) (string, error) {
	r.mu.Lock()
	r.calls++
	shouldPanic := !r.panicFirstOnly || r.calls == 1
	r.mu.Unlock()
	if shouldPanic {
		panic("prompt boom")
	}
	return "ok", nil
}

func (r *panicRunner) Abort() {}

// panicOnNthWriter 在第 target 次 Write 时 panic，其余帧正常透传给底层 writer。
// 用 io.Pipe 的写端作为底层 writer，可一边读取前面已写出的帧，一边把 panic
// 精确注入到 event/subscribe 的 "event" 通知写出路径。用互斥锁计数，因为
// peer 的写入发生在传输自己的 goroutine 中。
type panicOnNthWriter struct {
	mu     sync.Mutex
	n      int32
	target int32
	w      io.Writer
}

func (w *panicOnNthWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.n++
	hit := w.n == w.target
	w.mu.Unlock()
	if hit {
		panic("write boom")
	}
	return w.w.Write(p)
}

// panicLogCapture 捕获 logRecoveredPanic 的输出，供测试断言 panic 已被记录。
type panicLogCapture struct {
	mu   sync.Mutex
	msgs []string
	ch   chan string
}

func (c *panicLogCapture) logf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	c.mu.Lock()
	c.msgs = append(c.msgs, msg)
	c.mu.Unlock()
	if c.ch != nil {
		select {
		case c.ch <- msg:
		default:
		}
	}
}

// has 报告是否有日志包含 where 片段。
func (c *panicLogCapture) has(where string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, msg := range c.msgs {
		if strings.Contains(msg, where) {
			return true
		}
	}
	return false
}

func (c *panicLogCapture) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.msgs...)
}

// withPanicLogCapture 临时替换 panicLogf 为捕获实现。传输 goroutine 全部退出后
// 才由 t.Cleanup 恢复，避免与运行中 goroutine 的读写竞争。
func withPanicLogCapture(t *testing.T) *panicLogCapture {
	t.Helper()
	c := &panicLogCapture{ch: make(chan string, 8)}
	prev := panicLogf
	panicLogf = c.logf
	t.Cleanup(func() { panicLogf = prev })
	return c
}

// TestRPCPromptPanicRecovered 验证 --rpc 的 prompt goroutine panic 后：
// 进程不崩、客户端收到 -32603 错误回包、panic 被记录、传输正常退出。
func TestRPCPromptPanicRecovered(t *testing.T) {
	cap := withPanicLogCapture(t)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- RunRPC(context.Background(), inR, outW, Config{Runner: &panicRunner{}})
	}()

	enc := json.NewEncoder(inW)
	dec := json.NewDecoder(outR)
	_ = enc.Encode(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "prompt",
		"params": map[string]any{"text": "hi"},
	})
	resp := readJSONSoon(t, dec)
	if resp["id"] != float64(1) {
		t.Fatalf("response id = %v, want 1", resp["id"])
	}
	errBody, _ := resp["error"].(map[string]any)
	if errBody["code"] != float64(codeInternalError) {
		t.Fatalf("panic prompt response = %v, want -32603", resp)
	}
	if !cap.has("rpc prompt") {
		t.Fatalf("panic was not logged: %v", cap.snapshot())
	}

	_ = inW.Close()
	waitFor(t, done)
}

// TestACPPromptPanicRecovered 验证 ACP session/prompt 的 goroutine panic 后：
// 客户端收到 -32603，panic 被记录，且 busy 槽位被释放——紧随其后的第二次
// 提示必须被接受并返回 end_turn，而不是 "a prompt is already running"。
func TestACPPromptPanicRecovered(t *testing.T) {
	cap := withPanicLogCapture(t)
	runner := &panicRunner{panicFirstOnly: true}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- RunACP(context.Background(), inR, outW, Config{Runner: runner})
	}()

	enc := json.NewEncoder(inW)
	dec := json.NewDecoder(outR)
	_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "session/new"})
	created := readJSONSoon(t, dec)
	result, _ := created["result"].(map[string]any)
	sessionID, _ := result["sessionId"].(string)
	if sessionID == "" {
		t.Fatalf("session/new result = %v", created)
	}

	_ = enc.Encode(map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "session/prompt",
		"params": map[string]any{"sessionId": sessionID, "prompt": "hi"},
	})
	resp := readJSONSoon(t, dec)
	errBody, _ := resp["error"].(map[string]any)
	if errBody["code"] != float64(codeInternalError) {
		t.Fatalf("panic prompt response = %v, want -32603", resp)
	}
	if !cap.has("acp session/prompt") {
		t.Fatalf("panic was not logged: %v", cap.snapshot())
	}

	// 第二次提示：panic 路径必须已释放 busy，否则会收到 -32600。
	_ = enc.Encode(map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "session/prompt",
		"params": map[string]any{"sessionId": sessionID, "prompt": "hi"},
	})
	resp2 := readJSONSoon(t, dec)
	if errBody, _ := resp2["error"].(map[string]any); errBody != nil {
		t.Fatalf("second prompt after panic was rejected: %v", resp2)
	}
	body, _ := resp2["result"].(map[string]any)
	if body["stopReason"] != "end_turn" {
		t.Fatalf("second prompt result = %v", resp2)
	}

	_ = inW.Close()
	waitFor(t, done)
}

// TestRPCSubscribePanicRecovered 验证 event/subscribe goroutine 在写出 "event"
// 通知时 panic：进程不崩、panic 被记录，且 stop()/wg.Done 不被跳过——关闭 stdin
// 后传输必须正常退出（否则 RunRPC 的 wg.Wait 会永久阻塞）。
func TestRPCSubscribePanicRecovered(t *testing.T) {
	cap := withPanicLogCapture(t)
	hub := NewEventHub()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	// 第 1 帧是 subscribe 的 seq 结果，第 2 帧是触发 panic 的 event 通知。
	writer := &panicOnNthWriter{target: 2, w: outW}
	done := make(chan error, 1)
	go func() {
		done <- RunRPC(context.Background(), inR, writer, Config{Runner: &fakeRunner{}, Events: hub})
	}()

	enc := json.NewEncoder(inW)
	dec := json.NewDecoder(outR)
	_ = enc.Encode(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "event/subscribe",
		"params": map[string]any{"after": 0},
	})
	result := readJSONSoon(t, dec)
	if result["id"] != float64(1) {
		t.Fatalf("subscribe result = %v", result)
	}

	hub.Publish(agent.Event{Kind: agent.EventNotice, Text: "trigger"})

	select {
	case <-cap.ch:
	case <-time.After(2 * time.Second):
		t.Fatal("subscribe panic was not logged")
	}
	if !cap.has("rpc event/subscribe") {
		t.Fatalf("unexpected log: %v", cap.snapshot())
	}

	_ = inW.Close()
	waitFor(t, done)
}

// TestWireAgentEventsPanicRecovered 验证 WireAgentEvents 桥接 goroutine 在
// Publish panic 时（注入 nil hub 触发解引用 panic）仍能关闭 done 并记录日志，
// 等待桥接停止的调用方不会挂起。
func TestWireAgentEventsPanicRecovered(t *testing.T) {
	cap := withPanicLogCapture(t)
	emit := make(chan agent.Event, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// nil hub：Publish 解引用 nil 触发 panic，用于验证桥接的兜底恢复。
	var hub *EventHub
	done := WireAgentEvents(hub, emit, ctx)

	emit <- agent.Event{Kind: agent.EventNotice, Text: "trigger"}
	select {
	case <-done: // panic 后必须关闭，不能挂起
	case <-time.After(2 * time.Second):
		t.Fatal("bridge done channel was not closed after panic")
	}
	if !cap.has("wire agent events") {
		t.Fatalf("panic was not logged: %v", cap.snapshot())
	}
}

// TestRPCNormalPromptWireBaseline 固化 --rpc 正常提示路径的逐字节输出。
// recover 只挂在 panic 路径，正常路径的帧顺序与字节必须与此基线完全一致。
func TestRPCNormalPromptWireBaseline(t *testing.T) {
	runner := &fakeRunner{reply: "hello", chunks: []string{"hel", "lo"}}
	var in, out bytes.Buffer
	in.WriteString(`{"jsonrpc":"2.0","id":1,"method":"prompt","params":{"text":"hi"}}` + "\n")
	if err := RunRPC(context.Background(), &in, &out, Config{Runner: runner}); err != nil {
		t.Fatalf("RunRPC: %v", err)
	}
	want := "{\"jsonrpc\":\"2.0\",\"method\":\"prompt/delta\",\"params\":{\"text\":\"hel\"}}\n" +
		"{\"jsonrpc\":\"2.0\",\"method\":\"prompt/delta\",\"params\":{\"text\":\"lo\"}}\n" +
		"{\"id\":1,\"jsonrpc\":\"2.0\",\"result\":{\"text\":\"hello\"}}\n"
	if got := out.String(); got != want {
		t.Fatalf("wire bytes changed:\n got: %q\nwant: %q", got, want)
	}
}
