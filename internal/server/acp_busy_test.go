package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// gatedWriter 把每一帧暂存到可检查的缓冲区。当某帧含 marker 时，
// 它发出 entered 信号并阻塞直到被释放，使测试能在写入 goroutine
// 执行写之后的任何代码*之前*，
// 对终态响应的字节进行操作。
type gatedWriter struct {
	mu      sync.Mutex
	staged  bytes.Buffer
	marker  string
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newGatedWriter(marker string) *gatedWriter {
	return &gatedWriter{marker: marker, entered: make(chan struct{}), release: make(chan struct{})}
}

func (w *gatedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	n, err := w.staged.Write(p)
	w.mu.Unlock()
	if strings.Contains(string(p), w.marker) {
		w.once.Do(func() { close(w.entered) })
		<-w.release
	}
	return n, err
}

func (w *gatedWriter) snapshot() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.staged.String()
}

// gatedRunner 阻塞直到被释放，然后返回配置的结果。
type gatedRunner struct {
	started chan struct{}
	release chan struct{}
	err     error
	once    sync.Once
}

func (r *gatedRunner) Prompt(ctx context.Context, _ string, _ func(string)) (string, error) {
	r.once.Do(func() { close(r.started) })
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-r.release:
		return "ok", r.err
	}
}
func (r *gatedRunner) Abort() {}

func waitStagedContains(t *testing.T, w *gatedWriter, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(w.snapshot(), want) {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q in %q", want, w.snapshot())
}

// TestACPPromptFreesBusyBeforeTerminalResult 验证状态机
// 边界：一次提示的终态响应是客户端发送下一次的
// 信号。当那些字节上线时，服务器必须已经
// 释放单提示槽位（以及取消句柄），否则
// 流水线式发送下一次提示的严格客户端会被错误地以
// -32600 "a prompt is already running" 拒绝。
//
// 三条终态路径——end_turn、取消和真正的错误——必须
// 行为一致，因此该测试按 runner 结果表驱动。
func TestACPPromptFreesBusyBeforeTerminalResult(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		wantResult string
	}{
		{"end_turn", nil, `"stopReason":"end_turn"`},
		{"cancelled", context.Canceled, `"stopReason":"cancelled"`},
		{"failure", errors.New("boom"), `"code":-32603`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &gatedRunner{started: make(chan struct{}), release: make(chan struct{}), err: tc.err}
			inR, inW := io.Pipe()
			out := newGatedWriter(`"id":2`)
			defer inW.Close()

			done := make(chan error, 1)
			go func() {
				done <- RunACP(context.Background(), inR, out, Config{Runner: runner})
			}()

			enc := json.NewEncoder(inW)
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "session/new"})
			waitStagedContains(t, out, `"sessionId"`)

			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0", "id": 2, "method": "session/prompt",
				"params": map[string]any{"prompt": "first"},
			})
			<-runner.started
			close(runner.release)

			// 结果字节是在写入器被阻塞时暂存的，这正是
			// 客户端在能发送下一个提示之前观察到的内容。
			select {
			case <-out.entered:
			case <-time.After(2 * time.Second):
				t.Fatal("terminal result was never written")
			}
			waitStagedContains(t, out, `"id":2`)
			// 终态载荷必须保持其原始线协议含义：把
			// 清理移到写之前，不得把真正的失败变成
			// 取消（或反之）。
			if !strings.Contains(out.snapshot(), tc.wantResult) {
				t.Fatalf("terminal result = %s, want %q", out.snapshot(), tc.wantResult)
			}

			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0", "id": 3, "method": "session/prompt",
				"params": map[string]any{"prompt": "second"},
			})
			close(out.release)

			waitStagedContains(t, out, `"id":3`)
			if strings.Contains(out.snapshot(), "a prompt is already running") {
				t.Fatalf("next prompt was rejected by a stale busy flag: %s", out.snapshot())
			}
		})
	}
}
