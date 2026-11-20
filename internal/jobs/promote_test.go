package jobs

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeLive 代替由工具超时移交的 *proc.Process。仅仅提升并没有杀掉任何东西；观察者只对退出作出反应。完成观察者不得覆盖已杀死状态。这个 pid 几乎肯定已死；refreshLocked 必须把提升的作业留给其活观察者，而不是从 pid 猜测。
type fakeLive struct {
	pid  int
	code int
	done chan struct{}

	mu     sync.Mutex
	once   sync.Once
	killed bool
}

func newFakeLive(pid int) *fakeLive {
	return &fakeLive{pid: pid, done: make(chan struct{})}
}

func (f *fakeLive) Pid() int              { return f.pid }
func (f *fakeLive) Done() <-chan struct{} { return f.done }

func (f *fakeLive) ExitCode() int {
	<-f.done
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.code
}

func (f *fakeLive) finish(code int) {
	f.mu.Lock()
	f.code = code
	f.mu.Unlock()
	f.once.Do(func() { close(f.done) })
}

func (f *fakeLive) Kill() {
	f.mu.Lock()
	f.killed = true
	f.code = -1
	f.mu.Unlock()
	f.once.Do(func() { close(f.done) })
}

func (f *fakeLive) wasKilled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.killed
}

func TestPromoteFromTimeoutRegistersRunningJobAndWatchesExit(t *testing.T) {
	registry := newRegistry(t)
	logPath := filepath.Join(t.TempDir(), "job.log")
	if err := os.WriteFile(logPath, []byte("partial output\n"), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}
	live := newFakeLive(777)

	job, err := registry.PromoteFromTimeout("some-long-command", live, logPath)
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if job.ID != "job1" || job.Command != "some-long-command" {
		t.Fatalf("job = %+v", job)
	}
	if job.Status != StatusRunning || job.ExitCode != -1 {
		t.Fatalf("status = %q, exit = %d, want running/-1", job.Status, job.ExitCode)
	}
	if job.LogPath != logPath || job.PID != 777 || job.StartedAt.IsZero() {
		t.Fatalf("job = %+v", job)
	}
	listed, err := registry.List()
	if err != nil || len(listed) != 1 || listed[0].ID != job.ID {
		t.Fatalf("list = %+v, err = %v", listed, err)
	}
	if tail, err := registry.Tail(job.ID, 10); err != nil || !strings.Contains(tail, "partial output") {
		t.Fatalf("tail = %q, err = %v", tail, err)
	}

	// 仅仅提升并没有杀掉任何东西；观察者只对退出作出反应。
	if live.wasKilled() {
		t.Fatal("promotion must not kill the child")
	}
	live.finish(5)
	finished := waitForTerminal(t, registry, job.ID)
	if finished.Status != StatusExited || finished.ExitCode != 5 {
		t.Fatalf("finished = %+v, want exited/5", finished)
	}
}

func TestPromoteFromTimeoutStopKillsTheLiveChild(t *testing.T) {
	registry := newRegistry(t)
	live := newFakeLive(778)
	job, err := registry.PromoteFromTimeout("killing-test", live, filepath.Join(t.TempDir(), "job.log"))
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if err := registry.Stop(job.ID); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !live.wasKilled() {
		t.Fatal("Stop must reach the promoted child")
	}
	stopped, err := registry.Get(job.ID)
	if err != nil || stopped.Status != StatusKilled {
		t.Fatalf("stopped = %+v, err = %v", stopped, err)
	}
	// 完成观察者不得覆盖已杀死状态。
	time.Sleep(20 * time.Millisecond)
	stopped, err = registry.Get(job.ID)
	if err != nil || stopped.Status != StatusKilled {
		t.Fatalf("status after watcher = %+v, err = %v", stopped, err)
	}
}

func TestPromotedJobIsNotFlippedByPidRefresh(t *testing.T) {
	registry := newRegistry(t)
	// 这个 pid 几乎肯定已死；refreshLocked 必须把提升的作业
	// 留给其活观察者，而不是从 pid 猜测。
	live := newFakeLive(0x7FFFFFF1)
	job, err := registry.PromoteFromTimeout("refresh-test", live, filepath.Join(t.TempDir(), "job.log"))
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	listed, err := registry.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed) != 1 || listed[0].Status != StatusRunning {
		t.Fatalf("list = %+v, want a still-running promoted job", listed)
	}
	live.finish(0)
	waitForTerminal(t, registry, job.ID)
}
