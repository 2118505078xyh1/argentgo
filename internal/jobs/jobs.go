// Package jobs tracks background processes that outlive a turn.
//
// A job is a detached child whose stdout and stderr go to a log file under the
// registry directory. The registry itself is a JSON file, so the jobs of a
// previous session are still listed after a restart: a live pid is reported as
// running, a dead one as exited.
package jobs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Status is how a job is doing.
type Status string

const (
	// StatusRunning means the process is still alive.
	StatusRunning Status = "running"
	// StatusExited means it finished on its own.
	StatusExited Status = "exited"
	// StatusKilled means Stop (or Remove) terminated it.
	StatusKilled Status = "killed"
)

// Constants that shape the registry.
const (
	// DefaultTailLines is what Tail returns when asked for zero lines.
	DefaultTailLines = 50
	// storeFile is the JSON index beside the logs.
	storeFile = "jobs.json"
	// tailWindow bounds how much of a log Tail reads.
	tailWindow = 256 << 10
)

// Job is one background process, as it is persisted.
type Job struct {
	ID        string    `json:"id"`
	Command   string    `json:"command"`
	Args      []string  `json:"args,omitempty"`
	Dir       string    `json:"dir,omitempty"`
	StartedAt time.Time `json:"startedAt"`
	Status    Status    `json:"status"`
	// ExitCode is -1 while the job runs, and for a job whose exit code is no
	// longer recoverable (the process died while the registry was not watching).
	ExitCode int    `json:"exitCode"`
	LogPath  string `json:"logPath"`
	PID      int    `json:"pid"`

	// live is set on promoted jobs so Stop can signal the exact child instead
	// of relying only on a pid that an injected fake may not really own.
	live LiveProcess `json:"-"`
}

// Running reports whether the job is still going.
func (j Job) Running() bool { return j.Status == StatusRunning }

// Terminal reports whether the job has finished.
func (j Job) Terminal() bool { return j.Status != StatusRunning }

// Elapsed is how long ago the job started.
func (j Job) Elapsed() time.Duration {
	if j.StartedAt.IsZero() {
		return 0
	}
	return time.Since(j.StartedAt)
}

// Command describes the process to start.
type Command struct {
	// Program is the executable; it is not resolved through a shell.
	Program string
	// Args are passed unchanged.
	Args []string
	// Dir is the working directory; empty keeps the registry's current one.
	Dir string
	// Env entries ("K=V") are appended to the inherited environment.
	Env []string
}

// Registry owns the jobs of a session and their log files.
//
// Every method is safe to call from several goroutines; the JSON index is
// rewritten atomically after each change.
type Registry struct {
	dir string

	mu   sync.Mutex
	jobs map[string]*Job
	next int
}

// New opens (or creates) a registry whose logs live under dir.
func New(dir string) (*Registry, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("jobs: empty registry directory")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("jobs: resolve %q: %w", dir, err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("jobs: create %s: %w", abs, err)
	}
	r := &Registry{dir: abs, jobs: map[string]*Job{}}
	if err := r.load(); err != nil {
		return nil, err
	}
	return r, nil
}

// Dir is where the index and the logs live.
func (r *Registry) Dir() string { return r.dir }

// Start launches cmd detached and returns the recorded job.
func (r *Registry) Start(cmd Command) (Job, error) {
	if strings.TrimSpace(cmd.Program) == "" {
		return Job{}, errors.New("jobs: empty program")
	}
	id := r.allocateID()
	logPath := filepath.Join(r.dir, id+".log")

	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return Job{}, fmt.Errorf("jobs: open %s: %w", logPath, err)
	}

	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		_ = log.Close()
		_ = os.Remove(logPath)
		return Job{}, fmt.Errorf("jobs: open %s: %w", os.DevNull, err)
	}

	child := exec.Command(cmd.Program, cmd.Args...)
	child.Dir = cmd.Dir
	if len(cmd.Env) > 0 {
		child.Env = append(os.Environ(), cmd.Env...)
	}
	child.Stdin = devNull
	child.Stdout = log
	child.Stderr = log
	detach(child)

	if err := child.Start(); err != nil {
		_ = devNull.Close()
		_ = log.Close()
		_ = os.Remove(logPath)
		return Job{}, fmt.Errorf("jobs: spawn %q: %w", cmd.Program, err)
	}
	// The child holds its own copies now.
	_ = devNull.Close()
	_ = log.Close()

	job := Job{
		ID:        id,
		Command:   cmd.Program,
		Args:      append([]string(nil), cmd.Args...),
		Dir:       cmd.Dir,
		StartedAt: time.Now().UTC(),
		Status:    StatusRunning,
		ExitCode:  -1,
		LogPath:   logPath,
		PID:       child.Process.Pid,
	}

	r.mu.Lock()
	r.jobs[id] = &job
	saveErr := r.saveLocked()
	r.mu.Unlock()

	go r.reap(id, child)
	if saveErr != nil {
		return job, saveErr
	}
	return job, nil
}

// reap records the real exit code as soon as the child dies.
func (r *Registry) reap(id string, child *exec.Cmd) {
	_ = child.Wait()
	code := -1
	if child.ProcessState != nil {
		code = child.ProcessState.ExitCode()
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	job, ok := r.jobs[id]
	if !ok {
		return
	}
	if job.Status == StatusRunning {
		job.Status = StatusExited
	}
	job.ExitCode = code
	_ = r.saveLocked()
}

// List returns every job, oldest first, after refreshing dead processes.
func (r *Registry) List() ([]Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.refreshLocked() {
		if err := r.saveLocked(); err != nil {
			return nil, err
		}
	}
	out := make([]Job, 0, len(r.jobs))
	for _, job := range r.jobs {
		out = append(out, *job)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].StartedAt.Before(out[j].StartedAt)
	})
	return out, nil
}

// Get returns one job by id.
func (r *Registry) Get(id string) (Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.refreshLocked() {
		_ = r.saveLocked()
	}
	job, ok := r.jobs[id]
	if !ok {
		return Job{}, fmt.Errorf("jobs: no job named %q", id)
	}
	return *job, nil
}

// Stop kills a job and its descendants.
func (r *Registry) Stop(id string) error {
	r.mu.Lock()
	job, ok := r.jobs[id]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("jobs: no job named %q", id)
	}
	if job.Status != StatusRunning {
		r.mu.Unlock()
		return nil
	}
	job.Status = StatusKilled
	pid := job.PID
	live := job.live
	_ = r.saveLocked()
	r.mu.Unlock()

	if live != nil {
		live.Kill()
	} else {
		killTree(pid)
	}
	return nil
}

// Tail returns the last n lines of a job's log.
func (r *Registry) Tail(id string, n int) (string, error) {
	job, err := r.Get(id)
	if err != nil {
		return "", err
	}
	return tailFile(job.LogPath, n)
}

// Remove forgets a job and deletes its log.
func (r *Registry) Remove(id string) error {
	r.mu.Lock()
	job, ok := r.jobs[id]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("jobs: no job named %q", id)
	}
	running := job.Status == StatusRunning
	pid := job.PID
	live := job.live
	logPath := job.LogPath
	delete(r.jobs, id)
	_ = r.saveLocked()
	r.mu.Unlock()

	if running {
		if live != nil {
			live.Kill()
		} else {
			killTree(pid)
		}
	}
	if logPath != "" {
		if err := os.Remove(logPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("jobs: remove %s: %w", logPath, err)
		}
	}
	return nil
}

// Prune forgets finished jobs that started more than age ago.
//
// Their logs stay on disk: the log is the only trace of what a job did. An age
// of zero forgets every finished job.
func (r *Registry) Prune(age time.Duration) (int, error) {
	cutoff := time.Now().Add(-age)
	r.mu.Lock()
	defer r.mu.Unlock()
	removed := 0
	for id, job := range r.jobs {
		if job.Status == StatusRunning {
			continue
		}
		if age > 0 && job.StartedAt.After(cutoff) {
			continue
		}
		delete(r.jobs, id)
		removed++
	}
	if removed > 0 {
		if err := r.saveLocked(); err != nil {
			return removed, err
		}
	}
	return removed, nil
}

// refreshLocked flips jobs whose process vanished into exited. It reports
// whether anything changed. An exit code we can no longer recover stays -1.
func (r *Registry) refreshLocked() bool {
	changed := false
	for _, job := range r.jobs {
		if job.Status != StatusRunning {
			continue
		}
		// Promoted jobs are watched through their live handle, not a raw pid.
		if job.live != nil {
			continue
		}
		if alive(job.PID) {
			continue
		}
		job.Status = StatusExited
		job.ExitCode = -1
		changed = true
	}
	return changed
}

func (r *Registry) allocateID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	for {
		r.next++
		id := fmt.Sprintf("job%d", r.next)
		if _, taken := r.jobs[id]; !taken {
			return id
		}
	}
}

// index is the on-disk shape.
type index struct {
	Next int   `json:"next"`
	Jobs []Job `json:"jobs"`
}

func (r *Registry) indexPath() string { return filepath.Join(r.dir, storeFile) }

func (r *Registry) load() error {
	data, err := os.ReadFile(r.indexPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("jobs: read %s: %w", r.indexPath(), err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	var stored index
	if err := json.Unmarshal(data, &stored); err != nil {
		return fmt.Errorf("jobs: parse %s: %w", r.indexPath(), err)
	}
	for i := range stored.Jobs {
		job := stored.Jobs[i]
		r.jobs[job.ID] = &job
	}
	r.next = stored.Next
	return nil
}

func (r *Registry) saveLocked() error {
	stored := index{Next: r.next, Jobs: make([]Job, 0, len(r.jobs))}
	for _, job := range r.jobs {
		stored.Jobs = append(stored.Jobs, *job)
	}
	sort.Slice(stored.Jobs, func(i, j int) bool { return stored.Jobs[i].ID < stored.Jobs[j].ID })
	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return fmt.Errorf("jobs: encode index: %w", err)
	}
	data = append(data, '\n')
	tmp := r.indexPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("jobs: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, r.indexPath()); err != nil {
		return fmt.Errorf("jobs: replace %s: %w", r.indexPath(), err)
	}
	return nil
}

// tailFile reads the last n lines of a log without loading all of it.
func tailFile(path string, n int) (string, error) {
	if n <= 0 {
		n = DefaultTailLines
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("jobs: read %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()

	var data []byte
	if info, statErr := file.Stat(); statErr == nil && info.Size() > tailWindow {
		if _, err := file.Seek(-tailWindow, io.SeekEnd); err != nil {
			return "", fmt.Errorf("jobs: seek %s: %w", path, err)
		}
		data, err = io.ReadAll(io.LimitReader(file, tailWindow))
		if err != nil {
			return "", fmt.Errorf("jobs: read %s: %w", path, err)
		}
		// The window starts mid-line; drop that fragment.
		if cut := bytes.IndexByte(data, '\n'); cut >= 0 {
			data = data[cut+1:]
		}
	} else {
		data, err = io.ReadAll(file)
		if err != nil {
			return "", fmt.Errorf("jobs: read %s: %w", path, err)
		}
	}

	text := strings.TrimRight(string(data), "\r\n")
	if text == "" {
		return "", nil
	}
	lines := strings.Split(text, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n"), nil
}
