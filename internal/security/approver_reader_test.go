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

// gatedAnswerReader parks in Read until release is closed, then yields payload
// one byte at a time (the shape readLine expects). It reports the first entry
// into Read on entered so a test can synchronise on the reader being parked
// without sleeping.
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

// TestPromptApproverCancelledReaderDoesNotStealNextAnswer covers the reader
// leak behind a cancelled Ask: its goroutine stays parked in readLine holding
// readMu, so the following Ask must not read In concurrently with it.
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

	// Wait until the reader goroutine is genuinely parked in Read before
	// cancelling, so Ask really does leak a live reader holding readMu.
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

	// Ask returned but its reader is still parked holding readMu. Release it,
	// then wait until readMu is free before swapping in fresh input; that
	// proves the leaked readLine has finished and no longer touches p.In.
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

	// Release happened before this second Ask's read, so its goroutine can
	// acquire readMu and read the new input without deadlocking.
	p.In = strings.NewReader("y\n")
	ok, err := p.Ask(context.Background(), "shell", "echo hi")
	if err != nil {
		t.Fatalf("second Ask error = %v, want nil", err)
	}
	if !ok {
		t.Fatal("second Ask did not read the expected approval")
	}
}
