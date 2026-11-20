package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestChangedDetectsDiskEditAndReload is the core P1 scenario: a long-lived
// caller loads once, the operator edits a config file on disk, and without a
// restart Changed() flips true and Reload() yields the new value with a bumped
// generation.
func TestChangedDetectsDiskEditAndReload(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	path := filepath.Join(cwd, "argent.json")
	writeJSONFile(t, path, map[string]any{"model": "before"})

	env := envFunc(map[string]string{"HOME": home})
	cfg, err := Load(cwd, env)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Model != "before" {
		t.Fatalf("model = %q, want before", cfg.Model)
	}
	if gen := cfg.Generation(); gen != 1 {
		t.Fatalf("initial generation = %d, want 1", gen)
	}
	if cfg.Changed() {
		t.Fatal("Changed() true immediately after load with no disk edit")
	}

	// Use a different-length value so the size changes too; mtime alone can be
	// coarse on some filesystems.
	writeJSONFile(t, path, map[string]any{"model": "a-considerably-longer-after-model"})
	if !cfg.Changed() {
		t.Fatal("Changed() false after editing config on disk")
	}

	next, err := cfg.Reload()
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if next.Model != "a-considerably-longer-after-model" {
		t.Errorf("reloaded model = %q, want the new value", next.Model)
	}
	if next.Generation() != cfg.Generation()+1 {
		t.Errorf("generation = %d, want %d", next.Generation(), cfg.Generation()+1)
	}
	// The original snapshot is untouched.
	if cfg.Model != "before" {
		t.Errorf("receiver mutated: model = %q, want before", cfg.Model)
	}
	// After a successful reload, the new snapshot is clean again.
	if next.Changed() {
		t.Error("Changed() true on freshly reloaded config")
	}
}

// TestChangedNoFalsePositiveWithoutEdit guards against noisy polling: repeated
// Changed() calls with no disk activity must stay false.
func TestChangedNoFalsePositiveWithoutEdit(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	writeJSONFile(t, filepath.Join(cwd, "argent.json"), map[string]any{"model": "stable"})
	cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for i := 0; i < 3; i++ {
		if cfg.Changed() {
			t.Fatalf("Changed() true on poll %d with no disk edit", i)
		}
	}
}

// TestChangedDetectsNewConfigFile covers the case where a previously missing
// layer appears (existence change), not just an edit to an existing file.
func TestChangedDetectsNewConfigFile(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Sources) != 0 {
		t.Fatalf("sources = %v, want none before creating a config file", cfg.Sources)
	}
	if cfg.Changed() {
		t.Fatal("Changed() true before any config file exists")
	}

	writeJSONFile(t, filepath.Join(cwd, "argent.json"), map[string]any{"model": "new-file-model"})
	if !cfg.Changed() {
		t.Fatal("Changed() false after a new config file was created")
	}
	next, err := cfg.Reload()
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if next.Model != "new-file-model" || len(next.Sources) != 1 {
		t.Errorf("new file not picked up: model=%q sources=%v", next.Model, next.Sources)
	}
}

// TestReloadKeepsOverridePriority verifies that Reload re-applies the original
// flag overrides, so the documented precedence is unchanged across reloads.
func TestReloadKeepsOverridePriority(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	path := filepath.Join(cwd, "argent.json")
	writeJSONFile(t, path, map[string]any{"model": "disk-before"})

	env := envFunc(map[string]string{"HOME": home, "ARGENT_LOCALE": "zh-CN"})
	cfg, err := LoadWithOverrides(cwd, env, map[string]any{"model": "flag-wins"})
	if err != nil {
		t.Fatalf("LoadWithOverrides: %v", err)
	}
	if cfg.Model != "flag-wins" || cfg.Locale != "zh-CN" {
		t.Fatalf("initial merge: model=%q locale=%q", cfg.Model, cfg.Locale)
	}

	writeJSONFile(t, path, map[string]any{"model": "disk-after", "locale": "en-US"})
	next, err := cfg.Reload()
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	// The flag override still outranks the edited file; env still outranks both
	// for locale. Only the underlying disk value (unshadowed) would change.
	if next.Model != "flag-wins" {
		t.Errorf("override lost on reload: model = %q", next.Model)
	}
	if next.Locale != "zh-CN" {
		t.Errorf("env lost on reload: locale = %q", next.Locale)
	}
}

// TestReloadReturnErrorOnMalformedDisk keeps reload failures non-destructive:
// the caller keeps using the current config if the on-disk edit is invalid.
func TestReloadReturnErrorOnMalformedDisk(t *testing.T) {
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
	if !cfg.Changed() {
		t.Fatal("Changed() false after corrupting the config file")
	}
	next, err := cfg.Reload()
	if err == nil {
		t.Fatal("Reload accepted a malformed config")
	}
	if next != nil {
		t.Errorf("Reload returned a non-nil config on error: %+v", next)
	}
	// The receiver is still the last known-good snapshot.
	if cfg.Model != "valid" || cfg.Generation() != 1 {
		t.Errorf("receiver damaged by failed reload: model=%q gen=%d", cfg.Model, cfg.Generation())
	}
}

// TestHandBuiltConfigHasNoChangeDetection documents the safety default for
// Config values created outside Load (as done in several app tests).
func TestHandBuiltConfigHasNoChangeDetection(t *testing.T) {
	cfg := Config{Model: "manual"}
	if cfg.Changed() {
		t.Error("Changed() true for a hand-built Config")
	}
	if cfg.Generation() != 0 {
		t.Errorf("generation = %d, want 0 for a hand-built Config", cfg.Generation())
	}
}
