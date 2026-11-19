package security

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

// gateReader blocks in Read until release is closed, so a test can cancel the
// context while Ask is genuinely stuck in the underlying stdin read.
type gateReader struct {
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func newGateReader() *gateReader {
	return &gateReader{entered: make(chan struct{}), release: make(chan struct{})}
}

func (g *gateReader) Read(p []byte) (int, error) {
	g.once.Do(func() { close(g.entered) })
	<-g.release
	return 0, io.EOF
}

func TestPromptApproverAskReturnsOnCancellation(t *testing.T) {
	cases := []struct {
		name         string
		cancelBefore bool
		withOutput   bool
	}{
		{"context cancelled before the prompt", true, true},
		{"context cancelled while blocked in the read", false, true},
		{"pre-cancelled context with no output writer", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gate := newGateReader()
			defer close(gate.release) // let the (possibly leaked) read goroutine finish

			var out bytes.Buffer
			var w io.Writer
			if tc.withOutput {
				w = &out
			}
			approver := NewPromptApprover(gate, w)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			if tc.cancelBefore {
				cancel()
			}

			type result struct {
				ok  bool
				err error
			}
			done := make(chan result, 1)
			go func() {
				ok, err := approver.Ask(ctx, "bash", "{}")
				done <- result{ok: ok, err: err}
			}()

			if !tc.cancelBefore {
				select {
				case <-gate.entered:
				case <-time.After(2 * time.Second):
					t.Fatal("reader was never entered; Ask did not reach the blocking read")
				}
				cancel()
			}

			select {
			case got := <-done:
				if !errors.Is(got.err, context.Canceled) {
					t.Fatalf("Ask error = %v, want context.Canceled (ok=%v)", got.err, got.ok)
				}
				if got.ok {
					t.Fatal("a cancelled Ask must not approve the tool call")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("Ask did not return promptly after cancellation")
			}
		})
	}
}
