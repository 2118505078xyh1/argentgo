package config

import (
	"path/filepath"
	"testing"
)

func TestContextWindowDecode(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()

	// No context window keys: the field stays at its zero value.
	cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ContextWindow != 0 {
		t.Fatalf("absent contextWindow = %d, want 0", cfg.ContextWindow)
	}

	writeJSONFile(t, filepath.Join(cwd, "argent.json"), map[string]any{
		"contextWindow": 128_000,
	})
	cfg, err = Load(cwd, envFunc(map[string]string{"HOME": home}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ContextWindow != 128_000 {
		t.Fatalf("contextWindow = %d, want 128000", cfg.ContextWindow)
	}
}

func TestContextWindowFromModelAndProviderEntries(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	writeJSONFile(t, filepath.Join(cwd, "argent.json"), map[string]any{
		"model":    "my-model",
		"provider": "custom",
		"models": map[string]any{
			"my-model": map[string]any{"contextWindow": 200_000},
		},
		"providers": map[string]any{
			"custom": map[string]any{"contextWindow": 64_000},
		},
	})
	cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// The model entry wins over the provider entry.
	if cfg.ContextWindow != 200_000 {
		t.Fatalf("model contextWindow = %d, want 200000", cfg.ContextWindow)
	}
}

func TestAutoCondenseContextPercentDecode(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	writeJSONFile(t, filepath.Join(cwd, "argent.json"), map[string]any{
		"profiles": map[string]any{
			"fast":      map[string]any{"autoCondenseContextPercent": 60},
			"inherited": map[string]any{"autoCondenseContextPercent": -1},
			"plain":     map[string]any{"model": "m"},
		},
	})

	// Absent profile keeps the zero value.
	cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AutoCondenseContextPercent != 0 {
		t.Fatalf("absent percent = %d, want 0", cfg.AutoCondenseContextPercent)
	}

	// The active profile's percent is decoded into the field.
	cfg, err = Load(cwd, envFunc(map[string]string{"HOME": home, "ARGENT_PROFILE": "fast"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AutoCondenseContextPercent != 60 {
		t.Fatalf("profile percent = %d, want 60", cfg.AutoCondenseContextPercent)
	}
	if got := cfg.CondensePercent("fast"); got != 60 {
		t.Fatalf("CondensePercent(fast) = %d, want 60", got)
	}

	// -1 parses as an explicit "inherit global" marker.
	cfg, err = Load(cwd, envFunc(map[string]string{"HOME": home, "ARGENT_PROFILE": "inherited"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AutoCondenseContextPercent != -1 {
		t.Fatalf("negative percent = %d, want -1", cfg.AutoCondenseContextPercent)
	}
	if got := cfg.CondensePercent("inherited"); got != -1 {
		t.Fatalf("CondensePercent(inherited) = %d, want -1", got)
	}

	// A profile without the key reads as zero, not as another profile's value.
	cfg, err = Load(cwd, envFunc(map[string]string{"HOME": home, "ARGENT_PROFILE": "plain"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.CondensePercent("plain"); got != 0 {
		t.Fatalf("CondensePercent(plain) = %d, want 0", got)
	}

	// A flag-selected profile is read directly from the Profiles map even when
	// it is not the config's active profile.
	if got := cfg.CondensePercent("fast"); got != 60 {
		t.Fatalf("CondensePercent(fast) = %d, want 60", got)
	}
}
