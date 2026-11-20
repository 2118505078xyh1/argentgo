package llm

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// stagedReader yields pre-built SSE chunks, pausing before every chunk after
// the first. With hold set it blocks instead of reaching EOF, modelling a
// connection that stays open but goes silent.
type stagedReader struct {
	chunks  [][]byte
	pause   time.Duration
	hold    bool
	idx     int
	release chan struct{}
	once    sync.Once
}

func newStagedReader(hold bool, pause time.Duration, chunks ...string) *stagedReader {
	r := &stagedReader{hold: hold, pause: pause, release: make(chan struct{})}
	for _, c := range chunks {
		r.chunks = append(r.chunks, []byte(c))
	}
	return r
}

func (r *stagedReader) Read(p []byte) (int, error) {
	if r.idx >= len(r.chunks) {
		if r.hold {
			<-r.release
			r.hold = false // release once, then report EOF
			return 0, io.EOF
		}
		return 0, io.EOF
	}
	chunk := r.chunks[r.idx]
	r.idx++
	if r.idx > 1 && r.pause > 0 {
		time.Sleep(r.pause)
	}
	return copy(p, chunk), nil
}

func (r *stagedReader) unblock() { r.once.Do(func() { close(r.release) }) }

func sseDelta(content string) string {
	return `data: {"choices":[{"delta":{"content":"` + content + `"}}]}` + "\n\n"
}

const sseDone = "data: [DONE]\n\n"

func TestReadStreamIdleTimeoutFailsSilentConnection(t *testing.T) {
	r := newStagedReader(true, 0, sseDelta("hi"))
	defer r.unblock()

	client := &Client{IdleTimeout: 40 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	start := time.Now()
	msg, err := client.readStream(ctx, "m", r, nil)
	elapsed := time.Since(start)
	if !errors.Is(err, ErrStreamError) {
		t.Fatalf("err = %v, want ErrStreamError", err)
	}
	if !strings.Contains(err.Error(), "idle") {
		t.Fatalf("err = %q, want an idle-timeout message", err)
	}
	if msg.Content != "" {
		t.Fatalf("partial reply leaked on idle timeout: %+v", msg)
	}
	if elapsed > time.Second {
		t.Fatalf("idle timeout took %v, want it well under the 2s context deadline", elapsed)
	}
}

func TestReadStreamIdleTimeoutResetsOnActivity(t *testing.T) {
	// Ten 20ms gaps total 200ms, longer than the 150ms idle window: the read
	// must keep succeeding because each event pushes the deadline out.
	chunks := make([]string, 0, 11)
	for i := 0; i < 10; i++ {
		chunks = append(chunks, sseDelta("x"))
	}
	chunks = append(chunks, sseDone)
	r := newStagedReader(false, 20*time.Millisecond, chunks...)

	client := &Client{IdleTimeout: 150 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	msg, err := client.readStream(ctx, "m", r, nil)
	if err != nil {
		t.Fatalf("Chat readStream: %v", err)
	}
	if msg.Content != "xxxxxxxxxx" {
		t.Fatalf("content = %q, want ten deltas", msg.Content)
	}
}

func TestReadStreamZeroIdleTimeoutWaitsThroughSilence(t *testing.T) {
	// IdleTimeout == 0 must preserve the old behaviour: wait through the
	// 250ms gap until [DONE] instead of failing early.
	r := newStagedReader(false, 250*time.Millisecond, sseDelta("a"), sseDone)

	client := &Client{}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	start := time.Now()
	msg, err := client.readStream(ctx, "m", r, nil)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("readStream: %v", err)
	}
	if msg.Content != "a" {
		t.Fatalf("content = %q, want a", msg.Content)
	}
	if elapsed < 200*time.Millisecond {
		t.Fatalf("returned after %v, want to wait through the 250ms gap", elapsed)
	}
}
