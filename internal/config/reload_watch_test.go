package config

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestPollReloadPicksUpDiskChange verifies the happy path of the polling
// primitive: no change on a clean poll, then a disk edit is detected, reloaded
// into a new snapshot, and reported as changed. A follow-up poll of the new
// snapshot is clean (idempotence).
func TestPollReloadPicksUpDiskChange(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	path := filepath.Join(cwd, "argent.json")
	writeJSONFile(t, path, map[string]any{"model": "before"})

	cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	same, changed, err := PollReload(&cfg)
	if err != nil || changed || same != &cfg {
		t.Fatalf("clean poll = (%p, %v, %v), want (cfg, false, nil)", same, changed, err)
	}

	// Different length so the size changes too; mtime alone can be coarse.
	writeJSONFile(t, path, map[string]any{"model": "a-considerably-longer-after-model"})

	next, changed, err := PollReload(&cfg)
	if err != nil {
		t.Fatalf("PollReload: %v", err)
	}
	if !changed {
		t.Fatal("changed=false after a disk edit")
	}
	if next == &cfg {
		t.Error("PollReload returned the old snapshot pointer on a change")
	}
	if next.Model != "a-considerably-longer-after-model" {
		t.Errorf("reloaded model = %q, want the new value", next.Model)
	}
	if next.Generation() != cfg.Generation()+1 {
		t.Errorf("generation = %d, want %d", next.Generation(), cfg.Generation()+1)
	}
	// Receiver was not mutated.
	if cfg.Model != "before" || cfg.Generation() != 1 {
		t.Errorf("receiver damaged: model=%q gen=%d", cfg.Model, cfg.Generation())
	}

	// Idempotence: adopting next makes the next poll a no-op.
	again, changed, err := PollReload(next)
	if err != nil || changed || again != next {
		t.Errorf("poll of reloaded snapshot = (%p, %v, %v), want (next, false, nil)", again, changed, err)
	}
}

// TestPollReloadNoChangeKeepsSnapshot guards against noisy polling: repeated
// calls with no disk activity return the same snapshot, changed=false and nil
// error, without bumping the generation.
func TestPollReloadNoChangeKeepsSnapshot(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	writeJSONFile(t, filepath.Join(cwd, "argent.json"), map[string]any{"model": "stable"})

	cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for i := 0; i < 3; i++ {
		same, changed, err := PollReload(&cfg)
		if err != nil || changed || same != &cfg {
			t.Fatalf("poll %d = (%p, %v, %v), want (cfg, false, nil)", i, same, changed, err)
		}
	}
	if cfg.Generation() != 1 {
		t.Errorf("generation = %d, want 1 with no reloads", cfg.Generation())
	}
}

// TestPollReloadFailureKeepsLastGoodSnapshot keeps reload failures
// non-destructive: a malformed on-disk edit yields an error, changed=false and
// the original last-known-good snapshot, which stays usable.
func TestPollReloadFailureKeepsLastGoodSnapshot(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	path := filepath.Join(cwd, "argent.json")
	writeJSONFile(t, path, map[string]any{"model": "valid"})

	cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	same, changed, err := PollReload(&cfg)
	if err == nil {
		t.Fatal("PollReload accepted a malformed config")
	}
	if changed {
		t.Error("changed=true after a failed reload; nothing was adopted")
	}
	if same != &cfg {
		t.Error("PollReload replaced the snapshot on a failed reload")
	}
	if cfg.Model != "valid" || cfg.Generation() != 1 {
		t.Errorf("receiver damaged by failed reload: model=%q gen=%d", cfg.Model, cfg.Generation())
	}
}

// TestPollReloadGenerationIncrementsAcrossEdits exercises the long-lived usage
// pattern: adopt the returned snapshot, then poll again after another edit.
// Generation must climb 1 -> 2 -> 3 and overrides/env must survive each hop.
func TestPollReloadGenerationIncrementsAcrossEdits(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	path := filepath.Join(cwd, "argent.json")
	writeJSONFile(t, path, map[string]any{"model": "one"})

	env := envFunc(map[string]string{"HOME": home, "ARGENT_LOCALE": "zh-CN"})
	cfg, err := LoadWithOverrides(cwd, env, map[string]any{"model": "flag-wins"})
	if err != nil {
		t.Fatalf("LoadWithOverrides: %v", err)
	}
	if cfg.Generation() != 1 {
		t.Fatalf("initial generation = %d, want 1", cfg.Generation())
	}

	// adopt asserts the poll found a changed, error-free reload and returns the
	// snapshot to adopt. PollReload's multi-value return is its sole argument.
	adopt := func(next *Config, changed bool, err error) (*Config, error) {
		t.Helper()
		if err != nil {
			return nil, err
		}
		if !changed || next == nil {
			t.Fatalf("expected a change with a new snapshot, got (next=%v, changed=%v)", next, changed)
		}
		return next, nil
	}

	writeJSONFile(t, path, map[string]any{"model": "two-longer"})
	current, err := adopt(PollReload(&cfg))
	if err != nil {
		t.Fatalf("first reload: %v", err)
	}
	if current.Generation() != 2 {
		t.Fatalf("generation after first edit = %d, want 2", current.Generation())
	}

	writeJSONFile(t, path, map[string]any{"model": "three-longest"})
	current, err = adopt(PollReload(current))
	if err != nil {
		t.Fatalf("second reload: %v", err)
	}
	if current.Generation() != 3 {
		t.Fatalf("generation after second edit = %d, want 3", current.Generation())
	}
	// Original overrides and env still win over the edited disk file.
	if current.Model != "flag-wins" || current.Locale != "zh-CN" {
		t.Errorf("precedence lost across reloads: model=%q locale=%q", current.Model, current.Locale)
	}
}

// TestPollReloadConcurrent verifies the same *Config can be polled from many
// goroutines at once without races: each caller observes a consistent,
// changed snapshot with the bumped generation.
func TestPollReloadConcurrent(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	path := filepath.Join(cwd, "argent.json")
	writeJSONFile(t, path, map[string]any{"model": "before"})
	cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	writeJSONFile(t, path, map[string]any{"model": "a-longer-after-model"})

	const n = 16
	nexts := make([]*Config, n)
	changs := make([]bool, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			nexts[i], changs[i], errs[i] = PollReload(&cfg)
		}(i)
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Errorf("poll %d: %v", i, errs[i])
			continue
		}
		if !changs[i] || nexts[i] == &cfg {
			t.Errorf("poll %d did not adopt a new snapshot: changed=%v ptr=%p", i, changs[i], nexts[i])
			continue
		}
		if nexts[i].Model != "a-longer-after-model" || nexts[i].Generation() != 2 {
			t.Errorf("poll %d inconsistent: model=%q gen=%d", i, nexts[i].Model, nexts[i].Generation())
		}
	}
}

// TestPollReloadNoFingerprint documents the safety default for a nil Config
// and for Config values created outside Load: no fingerprint means no change
// detection, so they are returned unchanged with changed=false.
func TestPollReloadNoFingerprint(t *testing.T) {
	same, changed, err := PollReload(nil)
	if err != nil || changed || same != nil {
		t.Fatalf("nil poll = (%p, %v, %v), want (nil, false, nil)", same, changed, err)
	}

	handBuilt := Config{Model: "manual"}
	same, changed, err = PollReload(&handBuilt)
	if err != nil || changed || same != &handBuilt {
		t.Fatalf("hand-built poll = (%p, %v, %v), want (cfg, false, nil)", same, changed, err)
	}
}
