package security

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// gatedAnswerReader 在 Read 中停驻直到 release 被关闭，然后逐字节
// 产出 payload（readLine 期望的形式）。它在 entered 上报告第一次
// 进入 Read，使测试可以在 reader 已停驻这一点上同步，
// 而无需 sleep。
type gatedAnswerReader struct {
	entered     chan struct{}
	release     chan struct{}
	payload     []byte
	pos         int
	enteredOnce sync.Once
}

func newGatedAnswerReader(payload string) *gatedAnswerReader {
	return &gatedAnswerReader{
		entered: make(chan struct{}),
		release: make(chan struct{}),
		payload: []byte(payload),
	}
}

func (g *gatedAnswerReader) Read(p []byte) (int, error) {
	g.enteredOnce.Do(func() { close(g.entered) })
	<-g.release
	if g.pos >= len(g.payload) {
		return 0, io.EOF
	}
	p[0] = g.payload[g.pos]
	g.pos++
	return 1, nil
}

type promptAskResult struct {
	ok  bool
	err error
}

// TestPromptApproverCancelledReaderDoesNotStealNextAnswer 覆盖被取消的 Ask
// 背后的 reader 泄漏：其 goroutine 停在 readLine 中并持有
// readMu，因此后续 Ask 不得与它并发读取 In。
func TestPromptApproverCancelledReaderDoesNotStealNextAnswer(t *testing.T) {
	const timeout = 5 * time.Second

	first := newGatedAnswerReader("y\n")
	p := NewPromptApprover(first, io.Discard)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan promptAskResult, 1)
	go func() {
		ok, err := p.Ask(ctx, "shell", "echo hi")
		done <- promptAskResult{ok: ok, err: err}
	}()

	// 在取消之前，等待 reader goroutine 真正停在 Read 中，
	// 这样 Ask 确实会泄漏一个持有 readMu 的活 reader。
	select {
	case <-first.entered:
	case <-time.After(timeout):
		t.Fatal("first reader never entered Read")
	}
	cancel()

	select {
	case got := <-done:
		if !errors.Is(got.err, context.Canceled) {
			t.Fatalf("cancelled Ask error = %v, want context.Canceled", got.err)
		}
		if got.ok {
			t.Fatal("a cancelled Ask must not approve the tool call")
		}
	case <-time.After(timeout):
		t.Fatal("Ask did not return promptly after cancellation")
	}

	// Ask 已返回，但其 reader 仍停驻并持有 readMu。释放它，
	// 然后等 readMu 空闲后再换入新输入；这
	// 证明泄漏的 readLine 已经结束，不再触碰 p.In。
	close(first.release)

	lockDone := make(chan struct{})
	go func() {
		p.readMu.Lock()
		p.readMu.Unlock()
		close(lockDone)
	}()
	select {
	case <-lockDone:
	case <-time.After(timeout):
		t.Fatal("leaked reader never released readMu")
	}

	// 释放发生在这第二次 Ask 读取之前，因此其 goroutine 可以
	// 获取 readMu 并读取新输入，而不会死锁。
	p.In = strings.NewReader("y\n")
	ok, err := p.Ask(context.Background(), "shell", "echo hi")
	if err != nil {
		t.Fatalf("second Ask error = %v, want nil", err)
	}
	if !ok {
		t.Fatal("second Ask did not read the expected approval")
	}
}
