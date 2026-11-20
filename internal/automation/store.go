package automation

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// storeFile is the on-disk shape of the task list.
type storeFile struct {
	Version int    `json:"version"`
	Specs   []Spec `json:"specs"`
}

// Store is the durable task list: one JSON file in a directory, written
// atomically (temp file + rename) and guarded by a mutex so concurrent
// goroutines can read and write it safely.
type Store struct {
	mu    sync.Mutex
	dir   string
	file  string
	now   func() time.Time
	specs []Spec
}

// OpenStore loads (or creates) the task list in dir.
func OpenStore(dir string) (*Store, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, errors.New("automation: the store directory is empty")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("automation: create %s: %w", dir, err)
	}
	s := &Store{dir: dir, file: filepath.Join(dir, "automations.json"), now: time.Now}
	if err := s.reloadLocked(); err != nil {
		return nil, err
	}
	return s, nil
}

// Reload refreshes the in-memory task list from the on-disk JSON file. The
// store is a shared file: a separate process (for example `automation run`)
// can advance a slot or add a task while a daemon is running. Reloading lets
// the daemon observe those changes instead of acting on a stale cached view,
// which would fire an already-handled slot a second time.
func (s *Store) Reload() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reloadLocked()
}

// reloadLocked parses the JSON file into s.specs. The caller must hold s.mu.
// A missing file means an empty task list (a freshly created store).
func (s *Store) reloadLocked() error {
	data, err := os.ReadFile(s.file)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		s.specs = nil
		return nil
	case err != nil:
		return fmt.Errorf("automation: read %s: %w", s.file, err)
	}
	var file storeFile
	if err := json.Unmarshal(data, &file); err != nil {
		return fmt.Errorf("automation: %s is not a valid task list: %w", s.file, err)
	}
	var specs []Spec
	for _, spec := range file.Specs {
		if spec.ID == "" {
			spec.ID = slug(spec.Name)
		}
		if err := spec.Validate(); err != nil {
			return err
		}
		specs = append(specs, spec)
	}
	s.specs = specs
	return nil
}

// Dir returns the store directory.
func (s *Store) Dir() string { return s.dir }

// File returns the path of the JSON task list.
func (s *Store) File() string { return s.file }

// SetClock overrides the clock used to seed next-run times.
func (s *Store) SetClock(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if now != nil {
		s.now = now
	}
}

// List returns a copy of every task, in insertion order.
func (s *Store) List() []Spec {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Spec, len(s.specs))
	copy(out, s.specs)
	return out
}

// Get returns one task.
func (s *Store) Get(id string) (Spec, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.indexLocked(id)
	if idx < 0 {
		return Spec{}, false
	}
	return s.specs[idx], true
}

// Put validates and upserts a task, then persists the list. A task added
// without a next-run time is seeded with the next slot from the store clock.
func (s *Store) Put(spec Spec) error {
	if strings.TrimSpace(spec.ID) == "" {
		spec.ID = slug(spec.Name)
	}
	if err := spec.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if idx := s.indexLocked(spec.ID); idx >= 0 {
		// Keep the run history when the caller only supplied the definition.
		old := s.specs[idx]
		if spec.LastRun == nil {
			spec.LastRun = old.LastRun
		}
		if spec.LastStatus == "" {
			spec.LastStatus = old.LastStatus
		}
		if spec.LastLog == "" {
			spec.LastLog = old.LastLog
		}
		if spec.NextDue.IsZero() {
			if old.Schedule == spec.Schedule && !old.NextDue.IsZero() {
				spec.NextDue = old.NextDue
			} else {
				base := s.now()
				if spec.LastRun != nil {
					base = *spec.LastRun
				}
				spec.NextDue = spec.Schedule.Next(base)
			}
		}
		s.specs[idx] = spec
	} else {
		if spec.NextDue.IsZero() {
			spec.NextDue = spec.Schedule.Next(s.now())
		}
		s.specs = append(s.specs, spec)
	}
	return s.saveLocked()
}

// Delete removes a task and persists the list.
func (s *Store) Delete(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.indexLocked(id)
	if idx < 0 {
		return false, nil
	}
	s.specs = append(s.specs[:idx], s.specs[idx+1:]...)
	return true, s.saveLocked()
}

// SetEnabled flips a task on or off.
func (s *Store) SetEnabled(id string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.indexLocked(id)
	if idx < 0 {
		return fmt.Errorf("automation: no task %q", id)
	}
	s.specs[idx].Enabled = enabled
	if enabled && s.specs[idx].NextDue.IsZero() {
		s.specs[idx].NextDue = s.specs[idx].Schedule.Next(s.now())
	}
	return s.saveLocked()
}

// Due returns the enabled tasks whose next slot has passed.
func (s *Store) Due(now time.Time) []Spec {
	s.mu.Lock()
	defer s.mu.Unlock()
	var due []Spec
	for _, spec := range s.specs {
		if spec.IsDue(now) {
			due = append(due, spec)
		}
	}
	return due
}

// RecordRun stores the outcome of one run and advances the next slot past at.
// Manual runs have no scheduled slot, so the schedule resumes from at.
func (s *Store) RecordRun(id, status, logPath string, at time.Time) error {
	return s.recordRun(id, status, logPath, at, time.Time{})
}

// recordRun is RecordRun with the slot that triggered the run. When slot is
// known, the next firing advances from that slot instead of from the (possibly
// much later) completion time, so a slow task does not push the schedule
// forward. at is still the time recorded as LastRun.
func (s *Store) recordRun(id, status, logPath string, at, slot time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.indexLocked(id)
	if idx < 0 {
		return fmt.Errorf("automation: no task %q", id)
	}
	run := at
	s.specs[idx].LastRun = &run
	s.specs[idx].LastStatus = status
	s.specs[idx].LastLog = logPath
	base := slot
	if base.IsZero() {
		base = at
	}
	next := s.specs[idx].Schedule.Next(base)
	if !next.IsZero() && !next.After(at) {
		// The slot we just advanced to is already in the past (the run
		// started late or lasted longer than the interval). Resume from the
		// run time so a run_once task does not fire again on the next tick.
		next = s.specs[idx].Schedule.Next(at)
	}
	s.specs[idx].NextDue = next
	return s.saveLocked()
}

// Save rewrites the task list.
func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

func (s *Store) indexLocked(id string) int {
	for i, spec := range s.specs {
		if spec.ID == id {
			return i
		}
	}
	return -1
}

func (s *Store) saveLocked() error {
	data, err := json.MarshalIndent(storeFile{Version: 1, Specs: s.specs}, "", "  ")
	if err != nil {
		return fmt.Errorf("automation: encode %s: %w", s.file, err)
	}
	data = append(data, '\n')
	if err := writeAtomic(s.file, data); err != nil {
		return fmt.Errorf("automation: write %s: %w", s.file, err)
	}
	return nil
}

// writeAtomic writes data to a temporary file in the destination directory and
// renames it into place, so a reader never sees a half-written file.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}
