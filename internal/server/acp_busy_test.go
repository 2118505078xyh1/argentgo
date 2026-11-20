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

// gatedWriter stages every frame into an inspectable buffer. When a frame
// contains marker it signals entered and blocks until released, so a test can
// act on the bytes of a terminal response *before* the writing goroutine runs
// any code that follows the write.
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

// gatedRunner blocks until released, then returns the configured outcome.
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

// TestACPPromptFreesBusyBeforeTerminalResult verifies the state-machine
// boundary: the terminal response for one prompt is the client's cue to send
// the next. By the time those bytes are on the wire, the server must already
// have released the single-prompt slot (and the cancel handle), otherwise a
// strict client that pipelines the next prompt is spuriously rejected with
// -32600 "a prompt is already running".
//
// All three terminal paths — end_turn, cancellation and a real error — must
// behave identically, so the test is table-driven over the runner outcome.
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

			// The result bytes are staged while the writer is blocked, which is
			// exactly what a client observes before it can send the next prompt.
			select {
			case <-out.entered:
			case <-time.After(2 * time.Second):
				t.Fatal("terminal result was never written")
			}
			waitStagedContains(t, out, `"id":2`)
			// The terminal payload must keep its original wire meaning: moving
			// cleanup ahead of the write must not turn a real failure into a
			// cancellation (or vice versa).
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
