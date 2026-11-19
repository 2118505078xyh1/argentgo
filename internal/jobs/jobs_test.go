package jobs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func newRegistry(t *testing.T) *Registry {
	t.Helper()
	registry, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	return registry
}

// shellCommand builds a command line for the platform's shell.
func shellCommand(line string) Command {
	if runtime.GOOS == "windows" {
		return Command{Program: "cmd.exe", Args: []string{"/C", line}}
	}
	return Command{Program: "/bin/sh", Args: []string{"-c", line}}
}

// longCommand runs this test binary in a dedicated sleep mode. Using ping(1)
// here made the suite depend on an external tool and on pipe setup; on Windows
// that occasionally failed with ERROR_NO_DATA while the runner was busy.
func longCommand() Command {
	return Command{
		Program: os.Args[0],
		Args:    []string{"-test.run=^TestSleepHelperProcess$", "--"},
		Env:     []string{"ARGENT_JOBS_SLEEP_HELPER=1"},
	}
}

// TestSleepHelperProcess is only meaningful when launched by longCommand.
func TestSleepHelperProcess(t *testing.T) {
	if os.Getenv("ARGENT_JOBS_SLEEP_HELPER") != "1" {
		return
	}
	time.Sleep(60 * time.Second)
}

func waitForTerminal(t *testing.T, registry *Registry, id string) Job {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		job, err := registry.Get(id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		if job.Terminal() {
			return job
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job %s never finished", id)
	return Job{}
}

func waitForTail(t *testing.T, registry *Registry, id, needle string) string {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	last := ""
	for time.Now().Before(deadline) {
		tail, err := registry.Tail(id, 50)
		if err != nil {
			t.Fatalf("tail %s: %v", id, err)
		}
		last = tail
		if strings.Contains(tail, needle) {
			return tail
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("log of %s never contained %q, last tail: %q", id, needle, last)
	return ""
}

func TestStartCapturesOutputAndExitCode(t *testing.T) {
	registry := newRegistry(t)
	job, err := registry.Start(shellCommand("echo job-hello"))
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if job.ID != "job1" {
		t.Fatalf("id = %q, want job1", job.ID)
	}
	if job.PID <= 0 {
		t.Fatalf("pid = %d", job.PID)
	}
	if job.Status != StatusRunning {
		t.Fatalf("status = %q, want running", job.Status)
	}
	if want := filepath.Join(registry.Dir(), "job1.log"); job.LogPath != want {
		t.Fatalf("log path = %q, want %q", job.LogPath, want)
	}

	finished := waitForTerminal(t, registry, job.ID)
	if finished.Status != StatusExited {
		t.Fatalf("status = %q, want exited", finished.Status)
	}
	if finished.ExitCode != 0 {
		t.Fatalf("exit code = %d, want 0", finished.ExitCode)
	}
	if !strings.Contains(waitForTail(t, registry, job.ID, "job-hello"), "job-hello") {
		t.Fatalf("log missing the child output")
	}
}

func TestStartReportsANonZeroExit(t *testing.T) {
	registry := newRegistry(t)
	job, err := registry.Start(shellCommand("exit 4"))
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	finished := waitForTerminal(t, registry, job.ID)
	if finished.Status != StatusExited {
		t.Fatalf("status = %q, want exited", finished.Status)
	}
	if finished.ExitCode != 4 {
		t.Fatalf("exit code = %d, want 4", finished.ExitCode)
	}
}

func TestStopMarksTheJobKilled(t *testing.T) {
	registry := newRegistry(t)
	job, err := registry.Start(longCommand())
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = registry.Stop(job.ID) })

	if err := registry.Stop(job.ID); err != nil {
		t.Fatalf("stop: %v", err)
	}
	stopped, err := registry.Get(job.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if stopped.Status != StatusKilled {
		t.Fatalf("status = %q, want killed", stopped.Status)
	}

	deadline := time.Now().Add(20 * time.Second)
	for alive(job.PID) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if alive(job.PID) {
		t.Fatalf("pid %d is still running after Stop", job.PID)
	}
}

func TestTailKeepsTheLastLines(t *testing.T) {
	registry := newRegistry(t)
	line := "echo one& echo two& echo three& echo four& echo five"
	if runtime.GOOS != "windows" {
		line = "echo one; echo two; echo three; echo four; echo five"
	}
	job, err := registry.Start(shellCommand(line))
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	waitForTerminal(t, registry, job.ID)
	waitForTail(t, registry, job.ID, "five")

	tail, err := registry.Tail(job.ID, 2)
	if err != nil {
		t.Fatalf("tail: %v", err)
	}
	if !strings.Contains(tail, "five") {
		t.Fatalf("tail = %q", tail)
	}
	if strings.Contains(tail, "one") {
		t.Fatalf("tail kept the head: %q", tail)
	}
	all, err := registry.Tail(job.ID, 50)
	if err != nil {
		t.Fatalf("tail: %v", err)
	}
	if !strings.Contains(all, "one") || !strings.Contains(all, "five") {
		t.Fatalf("full tail = %q", all)
	}
}

func TestRemoveDeletesTheJobAndItsLog(t *testing.T) {
	registry := newRegistry(t)
	job, err := registry.Start(shellCommand("echo removable"))
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	waitForTerminal(t, registry, job.ID)

	if err := registry.Remove(job.ID); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := registry.Get(job.ID); err == nil {
		t.Fatal("a removed job must be gone")
	}
	if _, err := os.Stat(job.LogPath); !os.IsNotExist(err) {
		t.Fatalf("the log must be deleted, stat err = %v", err)
	}
}

func TestPruneForgetsFinishedJobsOnly(t *testing.T) {
	registry := newRegistry(t)
	done, err := registry.Start(shellCommand("echo done"))
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	waitForTerminal(t, registry, done.ID)

	running, err := registry.Start(longCommand())
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = registry.Stop(running.ID) })

	removed, err := registry.Prune(0)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if removed != 1 {
		t.Fatalf("prune removed %d jobs, want 1", removed)
	}
	jobs, err := registry.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(jobs) != 1 || jobs[0].ID != running.ID {
		t.Fatalf("list = %+v", jobs)
	}
	// Pruning keeps the log: it is the only trace of what the job did.
	if _, err := os.Stat(done.LogPath); err != nil {
		t.Fatalf("prune must keep the log: %v", err)
	}
	if _, err := registry.Prune(time.Hour); err != nil {
		t.Fatalf("prune with age: %v", err)
	}
}

func TestRegistryPersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	first, err := New(dir)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	job, err := first.Start(shellCommand("echo persisted"))
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	finished := waitForTerminal(t, first, job.ID)

	second, err := New(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	jobs, err := second.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("jobs = %+v", jobs)
	}
	if jobs[0].ID != job.ID || jobs[0].LogPath != job.LogPath {
		t.Fatalf("job = %+v", jobs[0])
	}
	if jobs[0].ExitCode != finished.ExitCode {
		t.Fatalf("exit code = %d, want %d", jobs[0].ExitCode, finished.ExitCode)
	}

	// A fresh id must not collide with the persisted one.
	next, err := second.Start(shellCommand("echo second"))
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if next.ID == job.ID {
		t.Fatalf("id %q was reused", next.ID)
	}
	waitForTerminal(t, second, next.ID)
}

func TestListRefreshesDeadPids(t *testing.T) {
	registry := newRegistry(t)
	// A job written by a process that is no longer running: its exit code is
	// unrecoverable, so it must report -1 rather than pretend.
	stored := index{Next: 1, Jobs: []Job{{
		ID:        "job1",
		Command:   "gone",
		StartedAt: time.Now().Add(-time.Hour).UTC(),
		Status:    StatusRunning,
		ExitCode:  -1,
		LogPath:   filepath.Join(registry.Dir(), "job1.log"),
		PID:       0x7FFFFFF0,
	}}}
	data, err := json.Marshal(stored)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(registry.Dir(), storeFile), data, 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
	reopened, err := New(registry.Dir())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	jobs, err := reopened.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("jobs = %+v", jobs)
	}
	if jobs[0].Status != StatusExited {
		t.Fatalf("status = %q, want exited", jobs[0].Status)
	}
	if jobs[0].ExitCode != -1 {
		t.Fatalf("exit code = %d, want -1", jobs[0].ExitCode)
	}
}

func TestConcurrentStartsGetUniqueIDs(t *testing.T) {
	registry := newRegistry(t)
	const count = 6
	var (
		wait  sync.WaitGroup
		mu    sync.Mutex
		ids   []string
		fails []string
	)
	for i := 0; i < count; i++ {
		wait.Add(1)
		go func(n int) {
			defer wait.Done()
			job, err := registry.Start(shellCommand(fmt.Sprintf("echo worker-%d", n)))
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				fails = append(fails, err.Error())
				return
			}
			ids = append(ids, job.ID)
		}(i)
	}
	wait.Wait()
	if len(fails) > 0 {
		t.Fatalf("start failures: %v", fails)
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
	listed, err := registry.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed) != count {
		t.Fatalf("listed %d jobs, want %d", len(listed), count)
	}
	for _, job := range listed {
		waitForTerminal(t, registry, job.ID)
	}
}

func TestUnknownIDsAreErrors(t *testing.T) {
	registry := newRegistry(t)
	if _, err := registry.Get("nope"); err == nil {
		t.Fatal("Get must fail")
	}
	if _, err := registry.Tail("nope", 10); err == nil {
		t.Fatal("Tail must fail")
	}
	if err := registry.Stop("nope"); err == nil {
		t.Fatal("Stop must fail")
	}
	if err := registry.Remove("nope"); err == nil {
		t.Fatal("Remove must fail")
	}
}

func TestStartRejectsAnEmptyProgram(t *testing.T) {
	registry := newRegistry(t)
	if _, err := registry.Start(Command{}); err == nil {
		t.Fatal("an empty program must be an error")
	}
}

func TestNewRejectsAnEmptyDirectory(t *testing.T) {
	if _, err := New("  "); err == nil {
		t.Fatal("an empty directory must be an error")
	}
}

func TestTailOfAMissingLogIsAnError(t *testing.T) {
	registry := newRegistry(t)
	job, err := registry.Start(shellCommand("echo gone-log"))
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	waitForTerminal(t, registry, job.ID)
	if err := os.Remove(job.LogPath); err != nil {
		t.Fatalf("remove log: %v", err)
	}
	if _, err := registry.Tail(job.ID, 5); err == nil {
		t.Fatal("a missing log must be an error")
	}
}
