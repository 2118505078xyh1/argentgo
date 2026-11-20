package jobs

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// startHelperChild spawns the sleep-helper test binary as a real child outside
// the registry. It stands for the innocent process that may inherit a stale
// job's pid after a crash, so the tests can prove it is never signalled.
func startHelperChild(t *testing.T) (*exec.Cmd, time.Time) {
	t.Helper()
	spec := longCommand()
	cmd := exec.Command(spec.Program, spec.Args...)
	cmd.Env = append(os.Environ(), spec.Env...)
	detach(cmd)
	startedAt := time.Now().UTC()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper child: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	// Reap in the background: kill(pid, 0) still succeeds for a zombie on
	// Unix, so an unreaped child would keep looking alive after Stop.
	go func() { _ = cmd.Wait() }()
	return cmd, startedAt
}

func writeJobIndex(t *testing.T, dir string, jobs ...Job) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create registry dir: %v", err)
	}
	data, err := json.Marshal(index{Next: len(jobs) + 1, Jobs: jobs})
	if err != nil {
		t.Fatalf("marshal index: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, storeFile), data, 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
}

func TestSameProcessRequiresMatchingCreationTime(t *testing.T) {
	cmd, startedAt := startHelperChild(t)
	pid := cmd.Process.Pid

	if !sameProcess(pid, startedAt) {
		t.Fatalf("sameProcess(%d, real start) = false, want true", pid)
	}
	if sameProcess(pid, startedAt.Add(-time.Hour)) {
		t.Fatal("an earlier start time must not identify the same pid")
	}
	if sameProcess(pid, startedAt.Add(time.Hour)) {
		t.Fatal("a future start time must not identify the same pid")
	}
	if sameProcess(pid, time.Time{}) {
		t.Fatal("a zero start time must fail closed")
	}
	if sameProcess(0x7FFFFFF0, startedAt) {
		t.Fatal("a dead pid must not be identified")
	}
	if sameProcess(0, startedAt) {
		t.Fatal("pid 0 must fail closed")
	}
}

// TestLoadRetiresAJobWhosePidWasRecycled checks the state right after reopening
// the index, before any Get/List refresh can run: load itself must not leave a
// recycled pid marked as running.
func TestLoadRetiresAJobWhosePidWasRecycled(t *testing.T) {
	cmd, realStart := startHelperChild(t)
	dir := t.TempDir()
	writeJobIndex(t, dir, Job{
		ID:        "job1",
		Command:   "unrelated",
		StartedAt: realStart.Add(-time.Hour),
		Status:    StatusRunning,
		ExitCode:  -1,
		PID:       cmd.Process.Pid,
		LogPath:   filepath.Join(dir, "job1.log"),
	})

	r, err := New(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	r.mu.Lock()
	loaded := *r.jobs["job1"]
	r.mu.Unlock()
	if loaded.Status != StatusExited || loaded.ExitCode != -1 {
		t.Fatalf("loaded job = %+v, want exited/-1", loaded)
	}
	got, err := r.Get("job1")
	if err != nil || got.Status != StatusExited {
		t.Fatalf("get = %+v, err = %v, want exited", got, err)
	}
	if !alive(cmd.Process.Pid) {
		t.Fatal("loading a registry must not signal the process")
	}
}

// TestRefreshRetiresAJobWhosePidWasRecycled injects the stale job directly, so
// refreshLocked (not load) is the code under test.
func TestRefreshRetiresAJobWhosePidWasRecycled(t *testing.T) {
	cmd, realStart := startHelperChild(t)
	r := newRegistry(t)
	r.mu.Lock()
	r.jobs["job1"] = &Job{
		ID:        "job1",
		Command:   "unrelated",
		StartedAt: realStart.Add(-time.Hour),
		Status:    StatusRunning,
		ExitCode:  -1,
		PID:       cmd.Process.Pid,
		LogPath:   filepath.Join(r.Dir(), "job1.log"),
	}
	r.mu.Unlock()

	got, err := r.Get("job1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != StatusExited || got.ExitCode != -1 {
		t.Fatalf("refreshed job = %+v, want exited/-1", got)
	}
	listed, err := r.List()
	if err != nil || len(listed) != 1 || listed[0].Status != StatusExited {
		t.Fatalf("list = %+v, err = %v, want one exited job", listed, err)
	}
	if !alive(cmd.Process.Pid) {
		t.Fatal("a refresh must not signal the process")
	}
}

func TestStopRefusesToKillARecycledPid(t *testing.T) {
	cmd, realStart := startHelperChild(t)
	r := newRegistry(t)
	r.mu.Lock()
	r.jobs["job1"] = &Job{
		ID:        "job1",
		Command:   "unrelated",
		StartedAt: realStart.Add(-time.Hour),
		Status:    StatusRunning,
		ExitCode:  -1,
		PID:       cmd.Process.Pid,
		LogPath:   filepath.Join(r.Dir(), "job1.log"),
	}
	r.mu.Unlock()

	err := r.Stop("job1")
	if !errors.Is(err, errProcessIdentity) {
		t.Fatalf("stop err = %v, want a process-identity refusal", err)
	}
	if !alive(cmd.Process.Pid) {
		t.Fatal("Stop must not kill a process with a recycled pid")
	}
	got, err := r.Get("job1")
	if err != nil || got.Status != StatusExited || got.ExitCode != -1 {
		t.Fatalf("job after refusal = %+v, err = %v, want exited/-1", got, err)
	}
}

func TestRemoveRefusesToKillARecycledPid(t *testing.T) {
	cmd, realStart := startHelperChild(t)
	r := newRegistry(t)
	r.mu.Lock()
	r.jobs["job1"] = &Job{
		ID:        "job1",
		Command:   "unrelated",
		StartedAt: realStart.Add(-time.Hour),
		Status:    StatusRunning,
		ExitCode:  -1,
		PID:       cmd.Process.Pid,
		LogPath:   filepath.Join(r.Dir(), "job1.log"),
	}
	r.mu.Unlock()

	err := r.Remove("job1")
	if !errors.Is(err, errProcessIdentity) {
		t.Fatalf("remove err = %v, want a process-identity refusal", err)
	}
	if !alive(cmd.Process.Pid) {
		t.Fatal("Remove must not kill a process with a recycled pid")
	}
	// The stale job is kept but retired, so a retry cannot kill anything.
	got, err := r.Get("job1")
	if err != nil || got.Status != StatusExited {
		t.Fatalf("job after refusal = %+v, err = %v, want a kept exited job", got, err)
	}
	if err := r.Remove("job1"); err != nil {
		t.Fatalf("retry remove: %v", err)
	}
	if !alive(cmd.Process.Pid) {
		t.Fatal("the retry must not kill the process either")
	}
}

// TestStopKillsAReloadedJobWithMatchingIdentity proves the check does not
// disable legitimate stops: only the raw-pid path of a reloaded job is used
// here, and its recorded start time matches the real child.
func TestStopKillsAReloadedJobWithMatchingIdentity(t *testing.T) {
	cmd, startedAt := startHelperChild(t)
	dir := t.TempDir()
	writeJobIndex(t, dir, Job{
		ID:        "job1",
		Command:   "helper",
		StartedAt: startedAt,
		Status:    StatusRunning,
		ExitCode:  -1,
		PID:       cmd.Process.Pid,
		LogPath:   filepath.Join(dir, "job1.log"),
	})

	r, err := New(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, err := r.Get("job1")
	if err != nil || got.Status != StatusRunning {
		t.Fatalf("matching job = %+v, err = %v, want running", got, err)
	}
	if err := r.Stop("job1"); err != nil {
		t.Fatalf("stop: %v", err)
	}
	stopped, err := r.Get("job1")
	if err != nil || stopped.Status != StatusKilled {
		t.Fatalf("stopped job = %+v, err = %v, want killed", stopped, err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for alive(cmd.Process.Pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if alive(cmd.Process.Pid) {
		t.Fatalf("pid %d is still running after Stop", cmd.Process.Pid)
	}
}
