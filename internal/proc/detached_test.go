package proc

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeChild is a process that can be told to stay alive past a timeout without
// launching a real OS process.
type fakeChild struct {
	pid  int
	code int
	// finishNow makes Wait complete immediately; otherwise it waits for done.
	finishNow bool
	done      chan struct{}
	closeOnce sync.Once
	mu        sync.Mutex
	killed    bool
	proc      *Process
}

func newFakeChild(pid, code int, finishNow bool) *fakeChild {
	return &fakeChild{pid: pid, code: code, finishNow: finishNow, done: make(chan struct{})}
}

func (f *fakeChild) Pid() int { return f.pid }

func (f *fakeChild) Wait() int {
	if f.finishNow {
		_, _ = f.proc.Write([]byte("fake-output\n"))
		f.closeOnce.Do(func() { close(f.done) })
		return f.code
	}
	<-f.done
	return f.code
}

func (f *fakeChild) Kill() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.killed {
		f.killed = true
		f.closeOnce.Do(func() { close(f.done) })
	}
}

func (f *fakeChild) wasKilled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.killed
}

// installFakeStarter swaps in a starter that returns one fake process. The
// returned pointer lets tests inspect what the fake saw after RunWithTimeout.
func installFakeStarter(t *testing.T, child *fakeChild, seed string) **Process {
	t.Helper()
	var p *Process
	SetStarter(func(Command) (*Process, error) {
		p = NewProcess(child)
		child.proc = p
		if seed != "" {
			_, _ = p.Write([]byte(seed))
		}
		return p, nil
	})
	t.Cleanup(func() { SetStarter(nil) })
	return &p
}

func TestRunWithTimeoutKeepsChildAliveWhenKeepOnTimeout(t *testing.T) {
	child := newFakeChild(4242, 7, false)
	slot := installFakeStarter(t, child, "partial-from-fake\n")

	res, live, err := RunWithTimeout(context.Background(), Command{
		Path:          "fake",
		Timeout:       15 * time.Millisecond,
		KeepOnTimeout: true,
	})
	var timeoutErr *TimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("err = %v, want *TimeoutError", err)
	}
	if !errors.Is(err, ErrTimeout) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want both ErrTimeout and DeadlineExceeded", err)
	}
	if live == nil || live != *slot {
		t.Fatalf("live = %v, want the started process", live)
	}
	if live.Pid() != 4242 {
		t.Fatalf("pid = %d, want 4242", live.Pid())
	}
	if child.wasKilled() {
		t.Fatal("KeepOnTimeout must not kill the child")
	}
	if !strings.Contains(res.Combined(), "partial-from-fake") {
		t.Fatalf("result lost the partial output: %q", res.Combined())
	}
	if !strings.Contains(live.Output(), "partial-from-fake") {
		t.Fatalf("live output lost the partial bytes: %q", live.Output())
	}

	// The promoted child is still alive; killing through its handle reaps it.
	live.Kill()
	select {
	case <-live.Done():
	case <-time.After(time.Second):
		t.Fatal("child never finished after Kill")
	}
	if live.ExitCode() != 7 {
		t.Fatalf("exit code = %d, want 7", live.ExitCode())
	}
}

func TestRunWithTimeoutKillsTreeOnTimeoutByDefault(t *testing.T) {
	child := newFakeChild(9, 4, false)
	installFakeStarter(t, child, "")

	res, live, err := RunWithTimeout(context.Background(), Command{
		Path:    "fake",
		Timeout: 15 * time.Millisecond,
	})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	if live != nil {
		t.Fatalf("live = %v, want nil without KeepOnTimeout", live)
	}
	if !child.wasKilled() {
		t.Fatal("default timeout must still kill the tree")
	}
	if res.ExitCode == 0 {
		t.Fatalf("a timed-out child must not report a clean exit: %+v", res)
	}
}

func TestRunWithTimeoutReturnsOutputAndExitCodeForAFastCommand(t *testing.T) {
	child := newFakeChild(11, 3, true)
	installFakeStarter(t, child, "")

	res, live, err := RunWithTimeout(context.Background(), Command{Path: "fake", Timeout: time.Second})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if live != nil {
		t.Fatalf("live = %v, want nil for a finished command", live)
	}
	if res.ExitCode != 3 {
		t.Fatalf("exit code = %d, want 3", res.ExitCode)
	}
	if !strings.Contains(res.Stdout, "fake-output") {
		t.Fatalf("stdout = %q", res.Stdout)
	}
}

func TestProcessSetOutputFlushesCapturedBytesAndRedirects(t *testing.T) {
	child := newFakeChild(12, 0, false)
	p := NewProcess(child)
	if _, err := p.Write([]byte("before\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	var buf bytes.Buffer
	p.SetOutput(&buf)
	if !strings.Contains(buf.String(), "before") {
		t.Fatalf("handoff lost the partial output: %q", buf.String())
	}
	if _, err := p.Write([]byte("after\n")); err != nil {
		t.Fatalf("write after handoff: %v", err)
	}
	if !strings.Contains(buf.String(), "after") {
		t.Fatalf("later output was not redirected: %q", buf.String())
	}
}
