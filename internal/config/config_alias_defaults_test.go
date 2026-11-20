package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// Compiled defaults must not shadow an alias spelling written in a config
// file. defaults() used to inject canonical keys (model, baseURL, ...) at the
// base layer; mergeTree then kept both spellings and decode read the canonical
// default first, silently discarding the file value. Diagnostics even reported
// a bogus "conflicting values ... using <canonical>" for the pair.
func TestAliasedFileKeysWinOverCompiledDefaults(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	store := filepath.Join(cwd, "store")
	writeJSONFile(t, filepath.Join(cwd, "argent.json"), map[string]any{
		"modelName":    "file-model",
		"base_url":     "https://file.test/v1",
		"api_key":      "sk-file",
		"sandbox_mode": SandboxReadOnly,
		"max_turns":    4,
		"storage_dir":  store,
	})
	cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Model != "file-model" {
		t.Errorf("model = %q, want file-model", cfg.Model)
	}
	if cfg.BaseURL != "https://file.test/v1" {
		t.Errorf("baseURL = %q, want the file alias value", cfg.BaseURL)
	}
	if cfg.APIKey != "sk-file" {
		t.Errorf("apiKey = %q, want the file alias value", cfg.APIKey)
	}
	if cfg.SandboxMode != SandboxReadOnly {
		t.Errorf("sandboxMode = %q, want the file alias value", cfg.SandboxMode)
	}
	if cfg.MaxTurns != 4 {
		t.Errorf("maxTurns = %d, want the file alias value", cfg.MaxTurns)
	}
	if cfg.StorageDir != store {
		t.Errorf("storageDir = %q, want the file alias value", cfg.StorageDir)
	}
	// A single spelling is not a conflict; the default is no longer a key.
	for _, d := range cfg.Diagnostics() {
		if d.Kind == DiagnosticConflict {
			t.Errorf("unexpected conflict diagnostic: %+v", d)
		}
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

// Both accepted spellings still work for every aliased field, and the
// canonical spelling keeps its documented preference inside one layer.
func TestAliasedDefaultsAcceptBothSpellings(t *testing.T) {
	cases := []struct {
		name      string
		file      map[string]any
		wantModel string
		wantBase  string
		wantKey   string
		wantSbx   string
		wantTurns int
		wantStore string
		wantDiags int // conflicts expected when both spellings are present
	}{
		{
			name:      "canonical spellings",
			file:      map[string]any{"model": "m", "baseURL": "u", "apiKey": "k", "sandboxMode": SandboxReadOnly, "maxTurns": 3, "storageDir": "s"},
			wantModel: "m", wantBase: "u", wantKey: "k", wantSbx: SandboxReadOnly, wantTurns: 3, wantStore: "s",
		},
		{
			name:      "snake_case and short aliases",
			file:      map[string]any{"modelName": "m", "base_url": "u", "api_key": "k", "sandbox_mode": SandboxReadOnly, "max_turns": 3, "storage_dir": "s"},
			wantModel: "m", wantBase: "u", wantKey: "k", wantSbx: SandboxReadOnly, wantTurns: 3, wantStore: "s",
		},
		{
			name:      "baseUrl middle spelling",
			file:      map[string]any{"baseUrl": "u"},
			wantBase:  "u",
			wantModel: defaultModel, wantSbx: SandboxWorkspaceWrite, wantTurns: defaultMaxTurns,
		},
		{
			name:      "both spellings in one file: canonical wins and conflict is reported",
			file:      map[string]any{"model": "canonical", "modelName": "alias"},
			wantModel: "canonical", wantBase: defaultBaseURL, wantSbx: SandboxWorkspaceWrite,
			wantTurns: defaultMaxTurns, wantDiags: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home, cwd := t.TempDir(), t.TempDir()
			writeJSONFile(t, filepath.Join(cwd, "argent.json"), tc.file)
			cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home}))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			wantStore := tc.wantStore
			if wantStore == "" {
				wantStore = filepath.Join(home, appDir)
			}
			if cfg.Model != tc.wantModel || cfg.BaseURL != tc.wantBase || cfg.APIKey != tc.wantKey ||
				cfg.SandboxMode != tc.wantSbx || cfg.MaxTurns != tc.wantTurns || cfg.StorageDir != wantStore {
				t.Errorf("decoded = {model:%q baseURL:%q apiKey:%q sandbox:%q turns:%d storage:%q}",
					cfg.Model, cfg.BaseURL, cfg.APIKey, cfg.SandboxMode, cfg.MaxTurns, cfg.StorageDir)
			}
			gotDiags := 0
			for _, d := range cfg.Diagnostics() {
				if d.Kind == DiagnosticConflict {
					gotDiags++
				}
			}
			if gotDiags != tc.wantDiags {
				t.Errorf("conflict diagnostics = %d, want %d: %+v", gotDiags, tc.wantDiags, cfg.Diagnostics())
			}
		})
	}
}

// Removing the compiled defaults must not mask an explicitly ill-typed value:
// the setting is still present, so decode yields the zero value and diagnostics
// report the bad type instead of quietly substituting the compiled default.
func TestAliasedIllTypedValueDoesNotFallBackToDefault(t *testing.T) {
	t.Run("modelName not a string", func(t *testing.T) {
		home, cwd := t.TempDir(), t.TempDir()
		writeJSONFile(t, filepath.Join(cwd, "argent.json"), map[string]any{"modelName": 42})
		cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home}))
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.Model != "" {
			t.Errorf("model = %q, want empty (no default fallback)", cfg.Model)
		}
		if err := cfg.Validate(); err == nil {
			t.Error("Validate accepted an empty model")
		}
		found := false
		for _, d := range cfg.Diagnostics() {
			if d.Kind == DiagnosticInvalid && strings.TrimPrefix(d.Path, "$.") == "modelName" {
				found = true
			}
		}
		if !found {
			t.Errorf("missing invalid diagnostic for modelName: %+v", cfg.Diagnostics())
		}
	})

	t.Run("max_turns not an integer", func(t *testing.T) {
		home, cwd := t.TempDir(), t.TempDir()
		writeJSONFile(t, filepath.Join(cwd, "argent.json"), map[string]any{"max_turns": "many"})
		cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home}))
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.MaxTurns != 0 {
			t.Errorf("maxTurns = %d, want 0 (no default fallback)", cfg.MaxTurns)
		}
		found := false
		for _, d := range cfg.Diagnostics() {
			if d.Kind == DiagnosticInvalid && strings.TrimPrefix(d.Path, "$.") == "max_turns" {
				found = true
			}
		}
		if !found {
			t.Errorf("missing invalid diagnostic for max_turns: %+v", cfg.Diagnostics())
		}
	})
}

// Env and flag layers write canonical keys; they must still beat an alias
// spelling from a file now that the default canonical key is absent.
func TestEnvAndOverrideBeatFileAlias(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	writeJSONFile(t, filepath.Join(cwd, "argent.json"), map[string]any{
		"modelName": "file-model",
		"max_turns": 4,
	})

	cfg, err := Load(cwd, envFunc(map[string]string{
		"HOME": home, "ARGENT_MODEL": "env-model", "ARGENT_MAX_TURNS": "7",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Model != "env-model" || cfg.MaxTurns != 7 {
		t.Errorf("env did not win over file aliases: model=%q turns=%d", cfg.Model, cfg.MaxTurns)
	}

	cfg, err = LoadWithOverrides(cwd, envFunc(map[string]string{"HOME": home}), map[string]any{
		"model": "flag-model", "maxTurns": 9,
	})
	if err != nil {
		t.Fatalf("LoadWithOverrides: %v", err)
	}
	if cfg.Model != "flag-model" || cfg.MaxTurns != 9 {
		t.Errorf("flag did not win over file aliases: model=%q turns=%d", cfg.Model, cfg.MaxTurns)
	}
}
