// Package jobs tracks background processes that outlive a turn.
//
// A job is a detached child whose stdout and stderr go to a log file under the
// registry directory. The registry itself is a JSON file, so the jobs of a
// previous session are still listed after a restart: a live pid is reported as
// running, a dead one as exited.
package jobs

import (
	"bytes"
	"context"
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
	// identityTolerance is the clock slack allowed when comparing a persisted
	// StartedAt with a live process's kernel creation time. Both timestamps
	// come from the same machine; the slack only covers the few milliseconds
	// between the two readings (plus /proc's 10 ms tick granularity on Unix).
	identityTolerance = 2 * time.Second
)

// errProcessIdentity marks a refusal to signal a pid whose creation time does
// not match the recorded job: after a crash the OS may have recycled that pid
// for an unrelated process.
var errProcessIdentity = errors.New("jobs: pid no longer matches the recorded start time")

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

	// watched marks jobs whose completion is tracked in this process by a
	// reaper (Start) or watcher (PromoteFromTimeout). refreshLocked must leave
	// these to that watcher: a child can die before the watcher records its
	// real exit code, and a pid-only refresh would otherwise publish a
	// terminal Exited/-1 snapshot in that window. Not persisted — a registry
	// reopened from disk has no watcher and must fall back to pid refresh.
	watched bool `json:"-"`
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
// Every method is safe to call from several goroutines. Two locks keep the
// in-memory state independent of disk I/O: mu guards jobs/next and is held only
// long enough to mutate state and copy a snapshot; ioMu serializes the actual
// JSON encoding and atomic replace. Readers never initiate a disk write — they
// only mark the registry dirty, and the next write path or the reaper persists
// the full snapshot. A version number discards snapshots that were overtaken
// by a newer state change.
//
// A reader may still wait for a write that is already in flight, so a status
// it observes is also on disk by the time it returns. That wait releases mu
// (sync.Cond.Wait), so it never blocks other readers or writers.
type Registry struct {
	dir string

	mu        sync.Mutex
	jobs      map[string]*Job
	next      int
	version   uint64 // guarded by mu; bumped on every in-memory state change
	dirty     bool   // guarded by mu; a state change has not reached the disk yet
	persisted uint64 // last version written to disk (read/written under ioMu+mu)
	inflight  int    // guarded by mu; number of persist calls in progress
	idleCond  *sync.Cond

	ioMu sync.Mutex // guards the encode + atomic replace of jobs.json

	// bgWork tracks completion watchers (the reaper spawned by Start and the
	// watcher spawned by PromoteFromTimeout). A watcher owns the final index
	// write for its job; waitIdle lets callers that are about to tear down the
	// registry directory make sure no background goroutine will write into it
	// afterwards. Add happens before the goroutine starts; tests call waitIdle
	// only after they are done starting jobs.
	bgWork sync.WaitGroup
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
	r.idleCond = sync.NewCond(&r.mu)
	if err := r.load(); err != nil {
		return nil, err
	}
	return r, nil
}

// Dir is where the index and the logs live.
func (r *Registry) Dir() string { return r.dir }

// waitIdle blocks until every completion watcher started by this registry has
// finished, including the final index write each one performs.
//
// Stop and Remove only signal the child: they return before its reaper has
// observed the exit and persisted the result. A caller that is about to delete
// the registry directory must wait here as well, or a watcher can create a
// .jobs-*.tmp file after the directory was scanned and break teardown with
// "directory not empty". Call waitIdle only once no more jobs will be started.
func (r *Registry) waitIdle() { r.bgWork.Wait() }

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
		watched:   true,
	}

	// Keep the returned value and the registry's pointer in SEPARATE storage:
	// storing &job would make the caller's returned copy alias the same memory
	// that reap() mutates under mu, which is a data race on Job.Status/ExitCode.
	stored := job
	r.mu.Lock()
	r.jobs[id] = &stored
	snap, version := r.changedLocked()
	r.mu.Unlock()

	// Encoding and writing happen without mu held: concurrent readers and
	// other writers are never blocked by the disk.
	saveErr := r.persist(snap, version)

	r.bgWork.Add(1)
	go func() {
		defer r.bgWork.Done()
		r.reap(id, child)
	}()
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
	job, ok := r.jobs[id]
	if !ok {
		r.mu.Unlock()
		return
	}
	// Stop may already have marked the job killed; never overwrite that
	// terminal state with an exit that the reaper happened to observe.
	if job.Status == StatusRunning {
		job.Status = StatusExited
	}
	job.ExitCode = code
	snap, version := r.changedLocked()
	r.mu.Unlock()
	_ = r.persist(snap, version)
}

// List returns every job, oldest first, after refreshing dead processes.
func (r *Registry) List() ([]Job, error) {
	r.mu.Lock()
	// A refresh only marks the registry dirty; no disk write happens here.
	r.refreshLocked()
	// Let any in-flight write land before copying. Cond.Wait releases mu, so
	// this never blocks another reader or writer. Copying after the wait keeps
	// the returned snapshot consistent with the state that reached the disk.
	for r.inflight > 0 {
		r.idleCond.Wait()
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
	r.mu.Unlock()
	return out, nil
}

// Get returns one job by id.
func (r *Registry) Get(id string) (Job, error) {
	r.mu.Lock()
	// A refresh only marks the registry dirty; no disk write happens here.
	r.refreshLocked()
	job, ok := r.jobs[id]
	// Let any in-flight write land before copying the snapshot (see List).
	for r.inflight > 0 {
		r.idleCond.Wait()
	}
	// Cond.Wait released mu, so a watcher may have updated (or a concurrent
	// caller may have removed) this job. Re-read the map and copy only now, so
	// the returned value matches the settled, persisted state.
	job, ok = r.jobs[id]
	var out Job
	if ok {
		out = *job
	}
	r.mu.Unlock()
	if !ok {
		return Job{}, fmt.Errorf("jobs: no job named %q", id)
	}
	return out, nil
}

// Stop kills a job and its descendants.
//
// A job reloaded from disk has no live handle, so its only reference to the
// child is a raw pid. Pids are recycled: after a crash the number may now
// belong to an unrelated process. Such a job must be re-identified by its
// creation time before it is signalled. A live handle is authoritative (it
// refers to the very child we started), so that path needs no pid check.
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
	pid := job.PID
	startedAt := job.StartedAt
	live := job.live
	if live == nil && !sameProcess(pid, startedAt) {
		// A watched job's terminal state and real exit code belong to its
		// in-process reaper/watcher. A dead or recycled pid must not be
		// published as Exited/-1 here — that is the same premature-terminal
		// race refreshLocked already avoids for watched jobs. Leave the job
		// running and let the watcher record the true code; we only refuse to
		// signal an unidentifiable pid.
		if job.watched {
			r.mu.Unlock()
			return fmt.Errorf("%w (job %s, pid %d); nothing was killed", errProcessIdentity, id, pid)
		}
		markStale(job)
		snap, version := r.changedLocked()
		r.mu.Unlock()
		_ = r.persist(snap, version)
		return fmt.Errorf("%w (job %s, pid %d); nothing was killed", errProcessIdentity, id, pid)
	}
	job.Status = StatusKilled
	snap, version := r.changedLocked()
	r.mu.Unlock()
	_ = r.persist(snap, version)

	if live != nil {
		live.Kill()
	} else {
		killTree(pid)
	}
	return nil
}

// StopAll signals every running job and waits for its completion watcher to
// reap it. Stop itself only signals the child; the reapers spawned by Start and
// the watchers spawned by PromoteFromTimeout record the real exit later. Waiting
// here means that when StopAll returns a nil error, no watched job is still
// being reaped.
//
// The wait is bounded by ctx: pass a context with a timeout to cap the total
// time spent. Every job is signalled before the wait begins, so an expired ctx
// never leaves a running job unsignalled — it only means a watcher did not
// finish in time. The returned error joins each individual Stop failure with
// ctx.Err() when the deadline fires.
//
// StopAll is idempotent and safe to call concurrently with readers and with
// other StopAll calls. It is meant for an explicit teardown: callers must not
// start new jobs while it runs, since the wait covers every watcher started so
// far. An empty (or already fully stopped) registry is a no-op.
func (r *Registry) StopAll(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	// Snapshot under mu: Stop mutates statuses and cannot be called under mu.
	r.mu.Lock()
	ids := make([]string, 0, len(r.jobs))
	for id, job := range r.jobs {
		if job.Status == StatusRunning {
			ids = append(ids, id)
		}
	}
	r.mu.Unlock()

	var errs []error
	for _, id := range ids {
		if err := r.Stop(id); err != nil {
			errs = append(errs, err)
		}
	}

	// waitIdle has no timeout, so drive it on a goroutine and bound this call
	// by ctx. A registry whose jobs all came from disk has no watchers, so this
	// returns at once.
	reaped := make(chan struct{})
	go func() {
		r.waitIdle()
		close(reaped)
	}()
	select {
	case <-reaped:
	case <-ctx.Done():
		errs = append(errs, ctx.Err())
	}
	return errors.Join(errs...)
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
//
// As with Stop, a reloaded job's raw pid is re-identified before it is
// signalled. On a mismatch nothing is killed: the job is retired instead and
// the error asks the caller to remove it again once the stale state is settled.
func (r *Registry) Remove(id string) error {
	r.mu.Lock()
	job, ok := r.jobs[id]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("jobs: no job named %q", id)
	}
	running := job.Status == StatusRunning
	pid := job.PID
	startedAt := job.StartedAt
	live := job.live
	logPath := job.LogPath
	if running && live == nil && !sameProcess(pid, startedAt) {
		// As in Stop, a watched job's terminal state belongs to its in-process
		// watcher: retiring it with -1 here would publish a premature terminal
		// snapshot and hide the real exit code the watcher is about to record.
		if job.watched {
			r.mu.Unlock()
			return fmt.Errorf("%w (job %s, pid %d); nothing was killed, job kept", errProcessIdentity, id, pid)
		}
		markStale(job)
		snap, version := r.changedLocked()
		r.mu.Unlock()
		_ = r.persist(snap, version)
		return fmt.Errorf("%w (job %s, pid %d); nothing was killed, job kept", errProcessIdentity, id, pid)
	}
	delete(r.jobs, id)
	snap, version := r.changedLocked()
	r.mu.Unlock()
	_ = r.persist(snap, version)

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
	var snap index
	var version uint64
	if removed > 0 {
		snap, version = r.changedLocked()
	}
	r.mu.Unlock()
	if removed > 0 {
		if err := r.persist(snap, version); err != nil {
			return removed, err
		}
	}
	return removed, nil
}

// refreshLocked flips jobs whose process vanished into exited. It reports
// whether anything changed. An exit code we can no longer recover stays -1.
//
// A refresh is a state change like any other: it bumps the version and marks
// the registry dirty. Read paths never write the index themselves — the next
// write path or the reaper persists the full snapshot.
func (r *Registry) refreshLocked() bool {
	changed := false
	for _, job := range r.jobs {
		if job.Status != StatusRunning {
			continue
		}
		// Jobs with an in-process watcher (Start reaper, promote watcher) own
		// their own exit code. A dead pid alone must not publish a terminal
		// Exited/-1 snapshot before that watcher has recorded the real code.
		if job.live != nil || job.watched {
			continue
		}
		// A live pid is not proof: it may have been recycled by the OS after
		// the registry was reloaded. Require the creation time to match too.
		if sameProcess(job.PID, job.StartedAt) {
			continue
		}
		markStale(job)
		changed = true
	}
	if changed {
		r.noteChangeLocked()
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
		// Jobs read back from disk have no live watcher and no pid capability.
		// Retire any "running" entry whose pid is gone or now belongs to a
		// different process, so a reader can never mistake a recycled pid for
		// the original child. No disk write happens here; the corrected state
		// is picked up by the next write path.
		if job.Status == StatusRunning && !sameProcess(job.PID, job.StartedAt) {
			markStale(&job)
		}
		r.jobs[job.ID] = &job
	}
	r.next = stored.Next
	return nil
}

// markStale retires a job whose process is gone or can no longer be identified
// with confidence. Its real exit code is unrecoverable, so it stays at -1.
func markStale(job *Job) {
	job.Status = StatusExited
	job.ExitCode = -1
}

// noteChangeLocked records an in-memory mutation. Caller must hold mu.
func (r *Registry) noteChangeLocked() {
	r.version++
	r.dirty = true
}

// changedLocked records a mutation and returns the snapshot to persist plus
// the version it represents. Caller must hold mu and must call persist after
// unlocking; no disk I/O happens while mu is held. It marks a write in flight
// so readers can wait for it to reach the disk.
func (r *Registry) changedLocked() (index, uint64) {
	r.noteChangeLocked()
	r.inflight++
	return r.snapshotLocked(), r.version
}

// finishWrite balances a changedLocked call. It is deferred by persist so the
// in-flight count is cleared on every return path, including errors.
func (r *Registry) finishWrite() {
	r.mu.Lock()
	r.inflight--
	if r.inflight == 0 {
		r.idleCond.Broadcast()
	}
	r.mu.Unlock()
}

// snapshotLocked copies the registry state for persistence. Caller must hold mu.
func (r *Registry) snapshotLocked() index {
	stored := index{Next: r.next, Jobs: make([]Job, 0, len(r.jobs))}
	for _, job := range r.jobs {
		stored.Jobs = append(stored.Jobs, *job)
	}
	sort.Slice(stored.Jobs, func(i, j int) bool { return stored.Jobs[i].ID < stored.Jobs[j].ID })
	return stored
}

// persist atomically writes a versioned snapshot to disk. Encoding and file
// I/O happen under ioMu but with mu released, so they never block readers or
// other writers of in-memory state. Snapshots overtaken by a newer version are
// skipped: the newer snapshot is written by its own caller.
func (r *Registry) persist(stored index, version uint64) error {
	defer r.finishWrite()
	r.ioMu.Lock()
	defer r.ioMu.Unlock()

	r.mu.Lock()
	stale := r.persisted >= version
	r.mu.Unlock()
	if stale {
		return nil
	}

	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return fmt.Errorf("jobs: encode index: %w", err)
	}
	data = append(data, '\n')

	// A unique temporary name avoids clobbering a file another registry
	// instance may be writing into the same directory.
	tmp, err := os.CreateTemp(r.dir, ".jobs-*.tmp")
	if err != nil {
		return fmt.Errorf("jobs: create temp index: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("jobs: write %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("jobs: close %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, r.indexPath()); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("jobs: replace %s: %w", r.indexPath(), err)
	}

	r.mu.Lock()
	if version > r.persisted {
		r.persisted = version
	}
	if r.version == version {
		r.dirty = false
	}
	r.mu.Unlock()
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
