package jobs

import (
	"errors"
	"testing"
	"time"
)

// TestStopAndRemoveLeaveWatchedExitCodeToWatcher pins down the division of
// responsibility for a job's terminal state. For a watched job (one with an
// in-process reaper/watchLive), the real exit code belongs to that watcher:
// a dead or recycled pid must never be turned into a premature Exited/-1 by
// Stop or Remove. The identity guard only refuses to signal an unidentifiable
// pid; it must leave the job running for the watcher. For an unwatched job
// (reloaded from disk, no watcher) there is nobody else to recover a code, so
// the existing retire-with-(-1) behaviour is preserved.
func TestStopAndRemoveLeaveWatchedExitCodeToWatcher(t *testing.T) {
	const deadPID = 0x7FFFFFF0 // almost certainly dead on both platforms
	const watcherCode = 7

	tests := []struct {
		name       string
		watched    bool
		op         string // "stop" or "remove"
		wantStatus Status
	}{
		{"stop-watched", true, "stop", StatusRunning},
		{"stop-unwatched", false, "stop", StatusExited},
		{"remove-watched", true, "remove", StatusRunning},
		{"remove-unwatched", false, "remove", StatusExited},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRegistry(t)
			job := &Job{
				ID:        "job1",
				Command:   "identity-test",
				StartedAt: time.Now().UTC(),
				Status:    StatusRunning,
				ExitCode:  -1,
				LogPath:   r.Dir() + "/job1.log",
				PID:       deadPID,
				watched:   tt.watched,
			}
			r.mu.Lock()
			r.jobs[job.ID] = job
			r.mu.Unlock()

			var err error
			switch tt.op {
			case "stop":
				err = r.Stop(job.ID)
			case "remove":
				err = r.Remove(job.ID)
			default:
				t.Fatalf("unknown op %q", tt.op)
			}
			if !errors.Is(err, errProcessIdentity) {
				t.Fatalf("%s err = %v, want a process-identity refusal", tt.op, err)
			}

			got, getErr := r.Get(job.ID)
			if getErr != nil {
				t.Fatalf("get: %v", getErr)
			}
			if got.Status != tt.wantStatus || got.ExitCode != -1 {
				t.Fatalf("after %s: status=%q exit=%d, want %q/-1",
					tt.op, got.Status, got.ExitCode, tt.wantStatus)
			}

			if !tt.watched {
				return
			}
			// The watcher still owns the terminal snapshot. Once it records the
			// real code, Get must report that code — never the -1 that a
			// premature retire here would have frozen in place.
			r.mu.Lock()
			stored := r.jobs[job.ID]
			stored.Status = StatusExited
			stored.ExitCode = watcherCode
			snap, version := r.changedLocked()
			r.mu.Unlock()
			_ = r.persist(snap, version)

			got, getErr = r.Get(job.ID)
			if getErr != nil {
				t.Fatalf("get after watcher: %v", getErr)
			}
			if got.Status != StatusExited || got.ExitCode != watcherCode {
				t.Fatalf("after watcher: status=%q exit=%d, want exited/%d",
					got.Status, got.ExitCode, watcherCode)
			}
		})
	}
}
