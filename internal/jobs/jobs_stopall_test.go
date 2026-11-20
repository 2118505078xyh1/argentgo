package jobs

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestStopAllStopsEveryRunningJob(t *testing.T) {
	registry := newRegistry(t)

	// A finished job must be left alone: StopAll only touches running jobs.
	done, err := registry.Start(shellCommand("echo stopall-done"))
	if err != nil {
		t.Fatalf("start done job: %v", err)
	}
	if finished := waitForTerminal(t, registry, done.ID); finished.Status != StatusExited {
		t.Fatalf("done job status = %q, want exited", finished.Status)
	}

	const running = 3
	ids := make([]string, 0, running)
	pids := make([]int, 0, running)
	for i := 0; i < running; i++ {
		job, err := registry.Start(longCommand())
		if err != nil {
			t.Fatalf("start job %d: %v", i, err)
		}
		ids = append(ids, job.ID)
		pids = append(pids, job.PID)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := registry.StopAll(ctx); err != nil {
		t.Fatalf("stop all: %v", err)
	}

	for _, id := range ids {
		job, err := registry.Get(id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		if job.Status != StatusKilled {
			t.Fatalf("job %s status = %q, want killed", id, job.Status)
		}
	}
	for _, pid := range pids {
		if alive(pid) {
			t.Fatalf("pid %d is still alive after StopAll", pid)
		}
	}

	// The job that had already exited keeps its terminal state.
	finished, err := registry.Get(done.ID)
	if err != nil {
		t.Fatalf("get done job: %v", err)
	}
	if finished.Status != StatusExited {
		t.Fatalf("finished job status = %q, want exited", finished.Status)
	}

	waitPersisted(t, registry)
}

func TestStopAllIsIdempotent(t *testing.T) {
	registry := newRegistry(t)
	job, err := registry.Start(longCommand())
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		_ = registry.Stop(job.ID)
		registry.waitIdle()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := registry.StopAll(ctx); err != nil {
		t.Fatalf("first stop all: %v", err)
	}
	first, err := registry.Get(job.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if err := registry.StopAll(ctx); err != nil {
		t.Fatalf("second stop all: %v", err)
	}
	second, err := registry.Get(job.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if second.Status != first.Status || second.ExitCode != first.ExitCode {
		t.Fatalf("second StopAll changed the job: first = %+v, second = %+v", first, second)
	}
	if second.Status != StatusKilled {
		t.Fatalf("status = %q, want killed", second.Status)
	}
}

func TestStopAllEmptyRegistryIsNoOp(t *testing.T) {
	registry := newRegistry(t)
	if err := registry.StopAll(context.Background()); err != nil {
		t.Fatalf("stop all on empty registry: %v", err)
	}
	jobs, err := registry.List()
	if err != nil || len(jobs) != 0 {
		t.Fatalf("list = %+v, err = %v, want empty", jobs, err)
	}
}

func TestStopAllConcurrentCallsAreSafe(t *testing.T) {
	registry := newRegistry(t)
	const n = 4
	started := make([]Job, 0, n)
	for i := 0; i < n; i++ {
		job, err := registry.Start(longCommand())
		if err != nil {
			t.Fatalf("start: %v", err)
		}
		started = append(started, job)
	}
	t.Cleanup(func() {
		for _, job := range started {
			_ = registry.Stop(job.ID)
		}
		registry.waitIdle()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- registry.StopAll(ctx)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent StopAll: %v", err)
		}
	}
	for _, job := range started {
		got, err := registry.Get(job.ID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if got.Status != StatusKilled {
			t.Fatalf("job %s status = %q, want killed", job.ID, got.Status)
		}
	}
}

// stubbornLive is a LiveProcess whose Kill deliberately leaves Done open, so
// its watcher never finishes and StopAll has to fall back to the ctx deadline.
type stubbornLive struct {
	pid  int
	done chan struct{}
}

func (s *stubbornLive) Pid() int              { return s.pid }
func (s *stubbornLive) Done() <-chan struct{} { return s.done }
func (s *stubbornLive) Kill()                 {}
func (s *stubbornLive) ExitCode() int         { return -1 }
func (s *stubbornLive) release()              { close(s.done) }

func TestStopAllRespectsContextDeadline(t *testing.T) {
	registry := newRegistry(t)
	live := &stubbornLive{pid: 12345, done: make(chan struct{})}
	t.Cleanup(live.release) // unblock the watcher at teardown

	job, err := registry.PromoteFromTimeout("stubborn", live, filepath.Join(t.TempDir(), "job.log"))
	if err != nil {
		t.Fatalf("promote: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = registry.StopAll(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stop all error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("StopAll took %v, the deadline should have bounded it", elapsed)
	}

	// The job was still signalled before the wait began.
	stopped, err := registry.Get(job.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if stopped.Status != StatusKilled {
		t.Fatalf("status = %q, want killed", stopped.Status)
	}
}
