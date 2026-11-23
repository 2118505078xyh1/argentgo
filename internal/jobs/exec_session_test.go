package jobs

import (
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeSessionHandle 是可在测试中精确控制输出与终止的进程替身。
type fakeSessionHandle struct {
	out    io.Writer
	mu     sync.Mutex
	writes []string
	done   chan struct{}
	killed atomic.Bool
}

func newFakeSessionHandle() *fakeSessionHandle {
	return &fakeSessionHandle{done: make(chan struct{})}
}

func (h *fakeSessionHandle) WriteStdin(p []byte) (int, error) {
	h.mu.Lock()
	h.writes = append(h.writes, string(p))
	h.mu.Unlock()
	_, _ = io.WriteString(h.out, "echo:"+string(p))
	return len(p), nil
}

func (h *fakeSessionHandle) Kill() {
	if h.killed.CompareAndSwap(false, true) {
		close(h.done)
	}
}

func (h *fakeSessionHandle) Done() <-chan struct{} { return h.done }
func (h *fakeSessionHandle) PID() int              { return 4242 }

func (h *fakeSessionHandle) input() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.writes...)
}

type fakeSessionRunner struct{ h *fakeSessionHandle }

func (r fakeSessionRunner) Start(out io.Writer) (SessionHandle, error) {
	r.h.out = out
	_, _ = io.WriteString(out, "ready\n")
	return r.h, nil
}

func TestExecSessionStartAndWriteStdin(t *testing.T) {
	h := newFakeSessionHandle()
	r := NewSessionRegistry()
	defer r.closeAll()

	info, out, err := r.Start(SessionStart{
		Command: "fake-repl",
		Stdin:   []byte("one\n"),
		Yield:   100 * time.Millisecond,
		Runner:  fakeSessionRunner{h: h},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if info.ID == "" || info.PID != 4242 || !info.Running {
		t.Fatalf("Start info = %+v", info)
	}
	if !strings.Contains(out, "ready") || !strings.Contains(out, "echo:one") {
		t.Fatalf("Start output = %q", out)
	}

	// 第二次写入只应返回新产生的内容，不重复首批输出。
	out, running, err := r.WriteStdin(info.ID, []byte("two\n"), 100*time.Millisecond)
	if err != nil {
		t.Fatalf("WriteStdin: %v", err)
	}
	if !running {
		t.Fatal("WriteStdin: session should still be running")
	}
	if strings.Contains(out, "echo:one") || !strings.Contains(out, "echo:two") {
		t.Fatalf("WriteStdin output = %q", out)
	}
	got := h.input()
	if len(got) != 2 || got[0] != "one\n" || got[1] != "two\n" {
		t.Fatalf("stdin writes = %q", got)
	}
}

func TestExecSessionCloseAndNotFound(t *testing.T) {
	h := newFakeSessionHandle()
	r := NewSessionRegistry()
	defer r.closeAll()

	info, _, err := r.Start(SessionStart{Command: "fake", Runner: fakeSessionRunner{h: h}})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := r.Close(info.ID); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !h.killed.Load() {
		t.Fatal("Close did not kill the process")
	}
	if err := r.Close(info.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("second Close = %v, want ErrSessionNotFound", err)
	}
	if _, _, err := r.WriteStdin(info.ID, []byte("x"), 0); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("WriteStdin after close = %v, want ErrSessionNotFound", err)
	}
}

func TestExecSessionIdleReap(t *testing.T) {
	h := newFakeSessionHandle()
	r := NewSessionRegistry()
	defer r.closeAll()
	r.idleTTL = 10 * time.Millisecond

	info, _, err := r.Start(SessionStart{Command: "fake", Runner: fakeSessionRunner{h: h}})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(30 * time.Millisecond)
	r.reapIdle()

	if !h.killed.Load() {
		t.Fatal("idle session was not killed")
	}
	r.mu.Lock()
	_, stillThere := r.sessions[info.ID]
	r.mu.Unlock()
	if stillThere {
		t.Fatal("idle session was not removed from the registry")
	}
}

func TestExecSessionStdinLimit(t *testing.T) {
	h := newFakeSessionHandle()
	r := NewSessionRegistry()
	defer r.closeAll()

	_, _, err := r.Start(SessionStart{
		Command: "fake",
		Stdin:   make([]byte, MaxSessionStdinBytes+1),
		Runner:  fakeSessionRunner{h: h},
	})
	if err == nil || !strings.Contains(err.Error(), "stdin exceeds") {
		t.Fatalf("Start with oversized stdin = %v", err)
	}
}

func TestSessionRingKeepsLatestBytes(t *testing.T) {
	ring := &sessionRing{}
	total := MaxSessionOutputBytes + 100
	_, _ = ring.Write([]byte(strings.Repeat("x", total)))
	data, pos := ring.snapshotFrom(0)
	if len(data) != MaxSessionOutputBytes {
		t.Fatalf("snapshot length = %d, want %d", len(data), MaxSessionOutputBytes)
	}
	if pos != uint64(total) {
		t.Fatalf("snapshot pos = %d, want %d", pos, total)
	}
	// 已消费位置之后的增量为空。
	data, _ = ring.snapshotFrom(pos)
	if data != "" {
		t.Fatalf("snapshot from end = %q, want empty", data)
	}
}
