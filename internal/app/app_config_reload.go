package app

import (
	"fmt"
	"sync"

	"github.com/2118505078xyh1/argentgo/internal/config"
)

// configReloader is the long-lived engine's hot-reload hook. It keeps the
// current config snapshot and polls it through config.PollReload at turn
// boundaries, atomically replacing the snapshot when the on-disk config changed.
//
// It is deliberately observation-only: adopting a fresh snapshot records the
// latest config (and the bumped generation) and logs the change, but it never
// re-resolves Resolved or rewires the LLM client mid-run. A run's Resolved is
// immutable for its lifetime by design, so applying a new config means starting
// a new run. Keeping the hook read-only this way means installing it cannot
// change execution semantics.
//
// One-shot execution never builds one (see the long-lived-only guard in
// runner.Prompt), so there is no pointless stat/reload work before the process
// exits.
//
// All state is mutex-guarded: the snapshot pointer is swapped under the lock,
// so a concurrent observer sees either the complete old snapshot or the new
// one, never a half-updated value.
type configReloader struct {
	mu      sync.Mutex
	current *config.Config
	logf    func(string)
}

// newConfigReloader returns a reloader seeded with cfg. logf may be nil, which
// silences change/failure notices; production passes a writer backed by the
// process diagnostic stream.
func newConfigReloader(cfg *config.Config, logf func(string)) *configReloader {
	return &configReloader{current: cfg, logf: logf}
}

// poll checks the on-disk config against the current snapshot and adopts the
// fresh one when config.PollReload reports a successful reload. A failed reload
// keeps the last known-good snapshot but still logs the error, so a broken
// operator edit is visible instead of silently ignored. The bool return reports
// whether a snapshot was adopted; it is informational only and drives no
// execution change.
func (r *configReloader) poll() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	cur := r.current
	next, changed, err := config.PollReload(cur)
	if err != nil {
		if r.logf != nil {
			r.logf(fmt.Sprintf("reload failed (%s); keeping last known-good snapshot: %v", configLabel(cur), err))
		}
		return false
	}
	if !changed || next == cur {
		return false
	}
	r.current = next
	if r.logf != nil {
		r.logf(fmt.Sprintf("reloaded: %s -> %s", configLabel(cur), configLabel(next)))
	}
	return true
}

// snapshot returns the currently adopted snapshot. A config snapshot is
// immutable once loaded and must not be mutated by callers.
func (r *configReloader) snapshot() *config.Config {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.current
}

func configLabel(cfg *config.Config) string {
	if cfg == nil {
		return "no config"
	}
	return fmt.Sprintf("generation %d, model %q", cfg.Generation(), cfg.Model)
}
