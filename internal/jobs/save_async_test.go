package jobs

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// hangGuard is a generous safety net so a regression cannot hang the whole
// suite. The actual choreography is done with channels, not with tight timing
// thresholds.
const hangGuard = 10 * time.Second

// waitPersisted waits until every in-memory change has reached the disk. It is
// used by tests that spawn reapers so directory teardown never races a write.
func waitPersisted(t *testing.T, r *Registry) {
	t.Helper()
	deadline := time.Now().Add(hangGuard)
	for {
		r.mu.Lock()
		caughtUp := r.persisted >= r.version && !r.dirty
		r.mu.Unlock()
		if caughtUp {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("registry did not persist its state")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestListDoesNotBlockAWrite checks the core of the refactor: while a disk
// write is in flight, List may wait for it but must release mu, so a concurrent
// Start/Get is never blocked. Handshakes, not timing thresholds, drive the test.
func TestListDoesNotBlockAWrite(t *testing.T) {
	r := newRegistry(t)

	// Hold ioMu to simulate a slow disk write. A Start will register its job
	// under mu, then block on ioMu inside persist.
	r.ioMu.Lock()

	startDone := make(chan error, 1)
	go func() {
		_, err := r.Start(shellCommand("echo async-list"))
		startDone <- err
	}()

	// Wait until Start has registered the job (it has bumped inflight and is
	// about to block on ioMu).
	deadline := time.Now().Add(hangGuard)
	for {
		r.mu.Lock()
		inserted := len(r.jobs) > 0
		r.mu.Unlock()
		if inserted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Start did not register its job")
		}
		runtime.Gosched()
	}

	// List will wait for the in-flight write to land.
	listDone := make(chan struct{})
	go func() {
		defer close(listDone)
		_, _ = r.List()
	}()

	// Give List time to enter its wait, then prove mu is available to another
	// caller — this is the guarantee a concurrent Start/Get depends on.
	runtime.Gosched()
	muAcquired := make(chan struct{})
	go func() {
		r.mu.Lock()
		r.mu.Unlock()
		close(muAcquired)
	}()
	select {
	case <-muAcquired:
		// mu was not held by List while it waited for the disk.
	case <-time.After(hangGuard):
		t.Fatal("List held mu while waiting for a disk write")
	}

	// Unblock the writer; both the List and the Start must then complete.
	r.ioMu.Unlock()
	select {
	case <-listDone:
	case <-time.After(hangGuard):
		t.Fatal("List did not finish after the write completed")
	}
	select {
	case err := <-startDone:
		if err != nil {
			t.Fatalf("start: %v", err)
		}
	case <-time.After(hangGuard):
		t.Fatal("Start deadlocked with a concurrent List")
	}

	jobs, err := r.List()
	if err != nil || len(jobs) != 1 {
		t.Fatalf("list = %+v, err = %v", jobs, err)
	}
	// Wait for Start's reaper too: Get waits for any in-flight write, so the
	// registry is fully quiet (and its directory safe to tear down) afterwards.
	waitForTerminal(t, r, jobs[0].ID)
	waitPersisted(t, r)
}

// TestListAndStartRunConcurrently stresses both paths together: no deadlock,
// no lost jobs, and a single consistent index at the end.
func TestListAndStartRunConcurrently(t *testing.T) {
	r := newRegistry(t)
	const starts = 6

	var wg sync.WaitGroup
	errs := make(chan error, starts*2)
	for i := 0; i < starts; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			if _, err := r.Start(shellCommand("echo worker-async")); err != nil {
				errs <- err
			}
		}(i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 30; j++ {
				if _, err := r.List(); err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent operation failed: %v", err)
	}

	listed, err := r.List()
	if err != nil || len(listed) != starts {
		t.Fatalf("listed %d jobs, want %d (err = %v)", len(listed), starts, err)
	}
	for _, job := range listed {
		waitForTerminal(t, r, job.ID)
	}
	waitPersisted(t, r)
}

// signalLive is a LiveProcess that can report when its ExitCode was read, so a
// test knows the completion watcher reached its decision point.
type signalLive struct {
	pid      int
	done     chan struct{}
	code     int
	codeRead chan struct{}
	doneOnce sync.Once
	codeOnce sync.Once
}

func newSignalLive(pid int) *signalLive {
	return &signalLive{pid: pid, done: make(chan struct{}), codeRead: make(chan struct{})}
}

func (s *signalLive) Pid() int              { return s.pid }
func (s *signalLive) Done() <-chan struct{} { return s.done }
func (s *signalLive) ExitCode() int {
	s.codeOnce.Do(func() { close(s.codeRead) })
	return s.code
}
func (s *signalLive) Kill() {
	s.doneOnce.Do(func() { close(s.done) })
}

// TestStoppedKilledStatusSurvivesWatchers covers both checked-under-mu sites.
// A watcher observing a child's exit must never turn a Stop-set killed status
// back into exited.
func TestStoppedKilledStatusSurvivesWatchers(t *testing.T) {
	t.Run("promoted watcher", func(t *testing.T) {
		r := newRegistry(t)
		live := newSignalLive(991)
		job, err := r.PromoteFromTimeout("watch-test", live, filepath.Join(t.TempDir(), "job.log"))
		if err != nil {
			t.Fatalf("promote: %v", err)
		}
		if err := r.Stop(job.ID); err != nil {
			t.Fatalf("stop: %v", err)
		}
		// Stop closes Done; the watcher now reads the exit code. Waiting for
		// that signal makes the check deterministic.
		select {
		case <-live.codeRead:
		case <-time.After(hangGuard):
			t.Fatal("completion watcher never ran")
		}
		got, err := r.Get(job.ID)
		if err != nil || got.Status != StatusKilled {
			t.Fatalf("status = %q, err = %v, want killed", got.Status, err)
		}
	})

	t.Run("start reaper", func(t *testing.T) {
		r := newRegistry(t)
		// Seed a killed job, then feed reap an already-waited child so it
		// records an exit without any process-timing dependency.
		r.mu.Lock()
		r.jobs["job1"] = &Job{ID: "job1", Status: StatusKilled, ExitCode: -1}
		r.mu.Unlock()

		cmd := exec.Command(os.Args[0], "-test.run=^$")
		if err := cmd.Run(); err != nil {
			t.Fatalf("run helper: %v", err)
		}
		r.reap("job1", cmd)

		got, err := r.Get("job1")
		if err != nil || got.Status != StatusKilled {
			t.Fatalf("status = %q, err = %v, want killed", got.Status, err)
		}
		waitPersisted(t, r)
	})
}

// TestIndexRoundTripAfterAsyncChanges checks that write-path persistence keeps
// the on-disk index consistent across a reopen, including a Stop-set status.
func TestIndexRoundTripAfterAsyncChanges(t *testing.T) {
	dir := t.TempDir()
	first, err := New(dir)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	live := newSignalLive(992)
	job, err := first.PromoteFromTimeout("persist-test", live, filepath.Join(dir, "job.log"))
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if err := first.Stop(job.ID); err != nil {
		t.Fatalf("stop: %v", err)
	}
	waitPersisted(t, first)

	second, err := New(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	reloaded, err := second.Get(job.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if reloaded.Status != StatusKilled {
		t.Fatalf("reloaded status = %q, want killed", reloaded.Status)
	}
	if reloaded.Command != job.Command || reloaded.LogPath != job.LogPath {
		t.Fatalf("reloaded job = %+v", reloaded)
	}

	// The next id must not collide with the persisted one.
	nextLive := newSignalLive(993)
	next, err := second.PromoteFromTimeout("second", nextLive, filepath.Join(dir, "job2.log"))
	if err != nil {
		t.Fatalf("promote second: %v", err)
	}
	if next.ID == job.ID {
		t.Fatalf("id %q was reused after reopen", next.ID)
	}
}

// TestConcurrentListsLeaveAtomicIndex checks the disk half of the refactor:
// concurrent readers never produce a half-written temp file, and every write
// leaves a complete, parseable index.
func TestConcurrentListsLeaveAtomicIndex(t *testing.T) {
	r := newRegistry(t)
	const starts = 8

	var wg sync.WaitGroup
	errs := make(chan error, starts*2)
	for i := 0; i < starts; i++ {
		wg.Add(2)
		go func(n int) {
			defer wg.Done()
			if _, err := r.Start(shellCommand("echo atomic")); err != nil {
				errs <- err
			}
		}(i)
		go func() {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				if _, err := r.List(); err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent operation failed: %v", err)
	}

	listed, err := r.List()
	if err != nil || len(listed) != starts {
		t.Fatalf("listed %d jobs, want %d (err = %v)", len(listed), starts, err)
	}
	for _, job := range listed {
		waitForTerminal(t, r, job.ID)
	}
	waitPersisted(t, r)

	data, err := os.ReadFile(filepath.Join(r.Dir(), storeFile))
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	var stored index
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatalf("index is not a complete JSON document: %v", err)
	}
	if len(stored.Jobs) != starts {
		t.Fatalf("index has %d jobs, want %d", len(stored.Jobs), starts)
	}

	entries, err := os.ReadDir(r.Dir())
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".jobs-") && strings.HasSuffix(name, ".tmp") {
			t.Fatalf("leftover temporary index file: %s", name)
		}
	}
}

// TestStaleSnapshotDoesNotOverwriteNewerState guards the version check: an
// older snapshot that reaches ioMu after a newer one must be skipped, not
// written over the fresher index.
func TestStaleSnapshotDoesNotOverwriteNewerState(t *testing.T) {
	r := newRegistry(t)

	r.mu.Lock()
	r.jobs["a"] = &Job{ID: "a", Status: StatusExited}
	r.jobs["b"] = &Job{ID: "b", Status: StatusExited}
	newSnap, newVersion := r.changedLocked()
	r.mu.Unlock()
	if err := r.persist(newSnap, newVersion); err != nil {
		t.Fatalf("persist newest: %v", err)
	}

	// A snapshot captured before "b" existed, but written afterwards. Go
	// through changedLocked so the in-flight accounting stays balanced.
	oldSnap := index{Jobs: []Job{{ID: "a", Status: StatusExited}}}
	r.mu.Lock()
	_, staleVersion := r.changedLocked()
	r.mu.Unlock()
	if err := r.persist(oldSnap, staleVersion-2); err != nil {
		t.Fatalf("persist stale: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(r.Dir(), storeFile))
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	var stored index
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatalf("index is not a complete JSON document: %v", err)
	}
	if len(stored.Jobs) != 2 {
		t.Fatalf("a stale snapshot overwrote the newer index: %+v", stored.Jobs)
	}
}
