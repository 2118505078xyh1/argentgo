package jobs

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// LiveProcess is the running child a tool timeout hands to the registry. It is
// structurally satisfied by *proc.Process; declaring it here keeps jobs from
// depending on proc.
type LiveProcess interface {
	Pid() int
	// Kill terminates the child and its descendants.
	Kill()
	// Done is closed once the child has been reaped.
	Done() <-chan struct{}
	// ExitCode is valid after Done is closed.
	ExitCode() int
}

// PromoteFromTimeout records an already-running child as a background job.
//
// logPath is the file the child is already writing to: it holds the output
// produced before the timeout and keeps receiving the rest. Promotion therefore
// neither re-runs the command nor loses its partial output. The returned job is
// registered as running; a watcher flips it to exited and records the exit code
// as soon as the child dies.
func (r *Registry) PromoteFromTimeout(command string, live LiveProcess, logPath string) (Job, error) {
	if strings.TrimSpace(command) == "" {
		return Job{}, errors.New("jobs: empty command")
	}
	if live == nil {
		return Job{}, errors.New("jobs: no live process")
	}
	id := r.allocateID()
	job := Job{
		ID:        id,
		Command:   command,
		StartedAt: time.Now().UTC(),
		Status:    StatusRunning,
		ExitCode:  -1,
		LogPath:   logPath,
		PID:       live.Pid(),
		live:      live,
		watched:   true,
	}

	// Separate storage for the returned value and the registry's pointer:
	// watchLive mutates the registry copy under mu while the caller reads the
	// returned Job. Aliasing them (storing &job) is a data race.
	stored := job
	r.mu.Lock()
	r.jobs[id] = &stored
	snap, version := r.changedLocked()
	r.mu.Unlock()

	r.bgWork.Add(1)
	go func() {
		defer r.bgWork.Done()
		r.watchLive(id, live)
	}()
	if saveErr := r.persist(snap, version); saveErr != nil {
		return job, fmt.Errorf("jobs: save %s: %w", id, saveErr)
	}
	return job, nil
}

// watchLive is the completion watcher for a promoted job. Stop already changed
// the status to killed, so it must not overwrite that terminal state.
func (r *Registry) watchLive(id string, live LiveProcess) {
	<-live.Done()
	code := live.ExitCode()

	r.mu.Lock()
	job, ok := r.jobs[id]
	if !ok || job.Status != StatusRunning {
		r.mu.Unlock()
		return
	}
	job.Status = StatusExited
	job.ExitCode = code
	snap, version := r.changedLocked()
	r.mu.Unlock()
	_ = r.persist(snap, version)
}
