package automation

import (
	"context"
	"testing"
	"time"
)

// TestDaemonRunDoesNotClobberConcurrentStoreEdits guards the write side of the
// shared-store story. Reloading at scan start (see daemon_reload_test.go) keeps
// a daemon from re-running a slot handled elsewhere, but the run outcome is
// later saved from the in-memory snapshot taken at scan time. If another
// process adds, edits or deletes a task while that run is in flight, saving the
// whole list from the stale snapshot silently drops the additions and
// resurrects the deletions. recordRun must refresh from disk before writing.
func TestDaemonRunDoesNotClobberConcurrentStoreEdits(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	now := mustTime(t, "2026-01-01T12:00:00Z")
	if err := store.Put(dueSpec("nightly", now.Add(-time.Hour))); err != nil {
		t.Fatalf("Put nightly: %v", err)
	}
	if err := store.Put(testSpec("doomed")); err != nil {
		t.Fatalf("Put doomed: %v", err)
	}

	// Block the executor so no further scan reload happens before the run
	// finishes: the only snapshot in play is the one scan() took above.
	started := make(chan struct{})
	release := make(chan struct{})
	exec := &fakeExecutor{run: func(ctx context.Context, spec Spec) (RunResult, error) {
		close(started)
		<-release
		return RunResult{Status: StatusOK}, nil
	}}
	d, err := newDaemon(Options{Store: store, Executor: exec, Clock: newFakeClock(now).Now})
	if err != nil {
		t.Fatalf("newDaemon: %v", err)
	}
	d.scan(context.Background())
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the executor never started")
	}

	// A separate process edits the shared task list while the run is in flight.
	other, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore (other): %v", err)
	}
	if err := other.Put(testSpec("added")); err != nil {
		t.Fatalf("Put added: %v", err)
	}
	if _, err := other.Delete("doomed"); err != nil {
		t.Fatalf("Delete doomed: %v", err)
	}

	close(release)
	d.wait()

	fresh, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if _, ok := fresh.Get("added"); !ok {
		t.Fatal("task added by another process was lost when the daemon saved its run")
	}
	if _, ok := fresh.Get("doomed"); ok {
		t.Fatal("task deleted by another process was resurrected when the daemon saved its run")
	}
	if got, ok := fresh.Get("nightly"); !ok || got.LastStatus != StatusOK {
		t.Fatalf("the daemon's own run outcome was not recorded: %+v (ok=%v)", got, ok)
	}
}
