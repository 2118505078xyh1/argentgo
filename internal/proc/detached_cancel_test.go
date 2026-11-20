package proc

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// signallingChild is a fake child that records terminate/kill requests. When
// cooperate is set it closes its wait channel on TerminateTree, modelling a
// child that flushes and exits on the graceful stop signal.
type signallingChild struct {
	pid        int
	cooperate  bool
	done       chan struct{}
	once       sync.Once
	mu         sync.Mutex
	terminated bool
	killed     bool
}

func (f *signallingChild) Pid() int { return f.pid }

func (f *signallingChild) Wait() int {
	<-f.done
	return 0
}

func (f *signallingChild) Kill() {
	f.mu.Lock()
	f.killed = true
	f.mu.Unlock()
	f.finish()
}

func (f *signallingChild) TerminateTree() {
	f.mu.Lock()
	f.terminated = true
	f.mu.Unlock()
	if f.cooperate {
		f.finish()
	}
}

func (f *signallingChild) finish() {
	f.once.Do(func() { close(f.done) })
}

func (f *signallingChild) requests() (terminated, killed bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.terminated, f.killed
}

func installSignallingStarter(t *testing.T, child *signallingChild) {
	t.Helper()
	SetStarter(func(Command) (*Process, error) {
		return NewProcess(child), nil
	})
	t.Cleanup(func() { SetStarter(nil) })
}

// TestRunWithTimeoutUserCancelTerminatesTreeBeforeEscalating guards the gap
// where the detached spawn path killed a cancelled child outright while Run
// gave one a SIGTERM grace period. A child that ignores the terminate request
// must still be killed, but only after the grace window.
func TestRunWithTimeoutUserCancelTerminatesTreeBeforeEscalating(t *testing.T) {
	old := cancellationGracePeriod
	cancellationGracePeriod = 80 * time.Millisecond
	defer func() { cancellationGracePeriod = old }()

	child := &signallingChild{pid: 1234, done: make(chan struct{})}
	installSignallingStarter(t, child)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	started := time.Now()
	res, live, err := RunWithTimeout(ctx, Command{Path: "fake"})
	elapsed := time.Since(started)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want Canceled", err)
	}
	if live != nil {
		t.Fatalf("live = %v, want nil after a cancellation", live)
	}
	if res.ExitCode != -1 {
		t.Fatalf("exit code = %d, want -1", res.ExitCode)
	}
	terminated, killed := child.requests()
	if !terminated {
		t.Fatal("child was never asked to terminate gracefully")
	}
	if !killed {
		t.Fatal("child that ignored the terminate request was not escalated to Kill")
	}
	if elapsed < 60*time.Millisecond {
		t.Fatalf("RunWithTimeout took %s; it must wait the grace period before escalating", elapsed)
	}
}

// TestRunWithTimeoutUserCancelStopsAtGracefulExit verifies the point of the
// grace period: a cooperative child must not receive Kill at all. The old
// behaviour failed this by killing immediately on context cancellation.
func TestRunWithTimeoutUserCancelStopsAtGracefulExit(t *testing.T) {
	old := cancellationGracePeriod
	cancellationGracePeriod = 3 * time.Second
	defer func() { cancellationGracePeriod = old }()

	child := &signallingChild{pid: 1235, cooperate: true, done: make(chan struct{})}
	installSignallingStarter(t, child)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	started := time.Now()
	_, _, err := RunWithTimeout(ctx, Command{Path: "fake"})
	elapsed := time.Since(started)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want Canceled", err)
	}
	terminated, killed := child.requests()
	if !terminated {
		t.Fatal("child was never asked to terminate gracefully")
	}
	if killed {
		t.Fatal("a cooperative child must not be killed after it exited on SIGTERM")
	}
	if elapsed >= time.Second {
		t.Fatalf("RunWithTimeout took %s; it must return as soon as the child exits gracefully", elapsed)
	}
}

// TestRunWithTimeoutDeadlineStillKillsImmediately keeps the timeout semantics
// intact: the two-phase stop is only for explicit user cancellation.
func TestRunWithTimeoutDeadlineStillKillsImmediately(t *testing.T) {
	old := cancellationGracePeriod
	cancellationGracePeriod = 3 * time.Second
	defer func() { cancellationGracePeriod = old }()

	child := &signallingChild{pid: 1236, done: make(chan struct{})}
	installSignallingStarter(t, child)

	_, _, err := RunWithTimeout(expiredContext{}, Command{Path: "fake", Timeout: 10 * time.Millisecond})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
	terminated, killed := child.requests()
	if terminated {
		t.Fatal("a deadline must not use the graceful terminate path")
	}
	if !killed {
		t.Fatal("a deadline must still kill the child")
	}
}
