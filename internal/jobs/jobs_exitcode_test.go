package jobs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// assertStoredWatched checks the registry's own copy: that is the one
// refreshLocked iterates, so the flag must be set there (not just on the value
// returned to the caller).
func assertStoredWatched(t *testing.T, r *Registry, id string) {
	t.Helper()
	r.mu.Lock()
	job, ok := r.jobs[id]
	watched := ok && job.watched
	r.mu.Unlock()
	if !watched {
		t.Fatalf("job %q must be marked watched", id)
	}
}

// TestStartAndPromoteMarkJobsWatched guards the fix at its source: both paths
// that spawn an in-process completion watcher must flag the stored job so
// refreshLocked leaves its exit code to that watcher.
func TestStartAndPromoteMarkJobsWatched(t *testing.T) {
	t.Run("start", func(t *testing.T) {
		r := newRegistry(t)
		job, err := r.Start(shellCommand("echo watched-start"))
		if err != nil {
			t.Fatalf("start: %v", err)
		}
		assertStoredWatched(t, r, job.ID)
		waitForTerminal(t, r, job.ID)
		waitPersisted(t, r)
	})

	t.Run("promote", func(t *testing.T) {
		r := newRegistry(t)
		live := newFakeLive(0x7FFFFFF1)
		job, err := r.PromoteFromTimeout("watched-promote", live, filepath.Join(t.TempDir(), "job.log"))
		if err != nil {
			t.Fatalf("promote: %v", err)
		}
		assertStoredWatched(t, r, job.ID)
		live.finish(0)
		waitForTerminal(t, r, job.ID)
		waitPersisted(t, r)
	})
}

// TestWatchedJobNeverShowsPrematureTerminalExit reproduces the race window
// deterministically: the child is already dead (unrecoverable pid) but the
// reaper has not yet recorded the real exit code. Readers must not publish a
// terminal Exited/-1 snapshot during that window — only the reaper may make the
// job terminal, and it carries the real code.
func TestWatchedJobNeverShowsPrematureTerminalExit(t *testing.T) {
	r := newRegistry(t)
	const realCode = 7
	stored := Job{
		ID:        "job1",
		Command:   "window-test",
		StartedAt: time.Now().UTC(),
		Status:    StatusRunning,
		ExitCode:  -1,
		LogPath:   filepath.Join(r.Dir(), "job1.log"),
		PID:       0x7FFFFFF2, // almost certainly dead on both platforms
		watched:   true,
	}
	r.mu.Lock()
	r.jobs[stored.ID] = &stored
	r.mu.Unlock()

	// Get must not turn the dead pid into a terminal -1 snapshot.
	got, err := r.Get(stored.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != StatusRunning || got.ExitCode != -1 {
		t.Fatalf("window Get = %+v, want running/-1 (no premature terminal)", got)
	}

	// Neither may List.
	listed, err := r.List()
	if err != nil || len(listed) != 1 {
		t.Fatalf("list = %+v, err = %v", listed, err)
	}
	if listed[0].Status != StatusRunning || listed[0].ExitCode != -1 {
		t.Fatalf("window List = %+v, want running/-1 (no premature terminal)", listed[0])
	}

	// Once the reaper records the real code, the terminal snapshot is valid
	// and carries that exact code, never the -1 sentinel.
	r.mu.Lock()
	job := r.jobs[stored.ID]
	job.Status = StatusExited
	job.ExitCode = realCode
	snap, version := r.changedLocked()
	r.mu.Unlock()
	_ = r.persist(snap, version)

	got, err = r.Get(stored.ID)
	if err != nil {
		t.Fatalf("get after reap: %v", err)
	}
	if got.Status != StatusExited || got.ExitCode != realCode {
		t.Fatalf("reaped Get = %+v, want exited/%d", got, realCode)
	}
}

// TestWatchedFlagIsNotPersisted keeps the on-disk JSON shape unchanged and
// verifies the intended consequence: a registry reopened from disk has no live
// watcher, so it must not inherit the marker and must still refresh dead pids.
func TestWatchedFlagIsNotPersisted(t *testing.T) {
	r := newRegistry(t)
	job, err := r.Start(shellCommand("echo persist-check"))
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	waitForTerminal(t, r, job.ID)
	waitPersisted(t, r)

	data, err := os.ReadFile(filepath.Join(r.Dir(), storeFile))
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if strings.Contains(string(data), `"watched"`) {
		t.Fatalf("index leaked the internal watched flag: %s", data)
	}

	second, err := New(r.Dir())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	second.mu.Lock()
	loaded, ok := second.jobs[job.ID]
	reloadedWatched := ok && loaded.watched
	second.mu.Unlock()
	if reloadedWatched {
		t.Fatal("a registry reopened from disk must not inherit the watched flag")
	}
}
