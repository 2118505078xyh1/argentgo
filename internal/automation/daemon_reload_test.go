package automation

import (
	"context"
	"testing"
	"time"
)

// TestDaemonDoesNotRerunSlotHandledByAnotherProcess guards the shared-store
// reload: a separate `automation run` process can handle a slot and advance
// NextDue on disk while this daemon's in-memory copy still shows the old slot.
// Without a refresh the daemon would claim and run that slot a second time.
func TestDaemonDoesNotRerunSlotHandledByAnotherProcess(t *testing.T) {
	dir := t.TempDir()
	daemonStore, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore (daemon): %v", err)
	}
	now := mustTime(t, "2026-01-01T12:00:00Z")
	if err := daemonStore.Put(dueSpec("nightly", now)); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Simulate a different process opening the same store file, running the
	// due task and advancing its next slot. Its writes go to disk only; the
	// daemon's cached Spec stays at the old 12:00 slot.
	other, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore (other): %v", err)
	}
	if err := other.RecordRun("nightly", StatusOK, "/tmp/nightly.log", now); err != nil {
		t.Fatalf("RecordRun (other): %v", err)
	}
	advanced, _ := other.Get("nightly")
	if !advanced.NextDue.After(now) {
		t.Fatalf("fixture did not advance NextDue past now: %s", advanced.NextDue)
	}

	exec := &fakeExecutor{}
	d, err := newDaemon(Options{Store: daemonStore, Executor: exec, Clock: newFakeClock(now).Now})
	if err != nil {
		t.Fatalf("newDaemon: %v", err)
	}
	d.scan(context.Background())
	d.wait()

	if exec.count() != 0 {
		t.Fatalf("daemon re-ran a slot already handled elsewhere: %d calls", exec.count())
	}
}

// TestDaemonReloadPicksUpTaskAddedByAnotherProcess is the positive counterpart:
// the refresh must also make tasks added to the file by another process visible
// to the very next scan, rather than suppressing all work.
func TestDaemonReloadPicksUpTaskAddedByAnotherProcess(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	now := mustTime(t, "2026-01-01T12:00:00Z")
	exec := &fakeExecutor{}
	d, err := newDaemon(Options{Store: store, Executor: exec, Clock: newFakeClock(now).Now})
	if err != nil {
		t.Fatalf("newDaemon: %v", err)
	}

	// Another process adds an already-due task after the daemon was created.
	other, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore (other): %v", err)
	}
	if err := other.Put(dueSpec("added", now.Add(-time.Minute))); err != nil {
		t.Fatalf("Put (other): %v", err)
	}

	d.scan(context.Background())
	d.wait()

	if exec.count() != 1 {
		t.Fatalf("executor called %d times, want 1 for the externally added task", exec.count())
	}
	if got, _ := store.Get("added"); got.LastStatus != StatusOK {
		t.Fatalf("last status = %q, want ok", got.LastStatus)
	}
}
