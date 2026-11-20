package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestPrivacyModeDefaultAny pins the compiled default: with no file, env or
// flag setting the posture is the historical permissive "any".
func TestPrivacyModeDefaultAny(t *testing.T) {
	cfg, err := Load(t.TempDir(), envFunc(map[string]string{"HOME": t.TempDir()}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PrivacyMode != PrivacyModeAny {
		t.Fatalf("PrivacyMode = %q, want %q", cfg.PrivacyMode, PrivacyModeAny)
	}
}

// TestPrivacyModeFromConfigFile covers both documented spellings. The value is
// canonicalized (trimmed, lower-case) so it matches the security enum exactly.
func TestPrivacyModeFromConfigFile(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  string
	}{
		{"camelCase", "privacyMode"},
		{"snake_case", "privacy_mode"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, cwd := t.TempDir(), t.TempDir()
			writeJSONFile(t, filepath.Join(cwd, "argent.json"), map[string]any{
				tc.key: " LOCAL-ONLY ",
			})
			cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home}))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.PrivacyMode != PrivacyModeLocalOnly {
				t.Fatalf("PrivacyMode = %q, want %q", cfg.PrivacyMode, PrivacyModeLocalOnly)
			}
		})
	}
}

// TestPrivacyModePrecedence pins file < env < flag for the new setting.
func TestPrivacyModePrecedence(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	writeJSONFile(t, filepath.Join(cwd, "argent.json"), map[string]any{
		"privacyMode": PrivacyModeLocalOnly,
	})
	baseEnv := envFunc(map[string]string{"HOME": home})

	// File wins over the default.
	cfg, err := Load(cwd, baseEnv)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PrivacyMode != PrivacyModeLocalOnly {
		t.Fatalf("file PrivacyMode = %q, want %q", cfg.PrivacyMode, PrivacyModeLocalOnly)
	}

	// ARGENT_PRIVACY_MODE beats the file.
	cfg, err = Load(cwd, envFunc(map[string]string{
		"HOME":                home,
		"ARGENT_PRIVACY_MODE": PrivacyModeAny,
	}))
	if err != nil {
		t.Fatalf("Load env: %v", err)
	}
	if cfg.PrivacyMode != PrivacyModeAny {
		t.Fatalf("env PrivacyMode = %q, want %q", cfg.PrivacyMode, PrivacyModeAny)
	}

	// A flag/override beats both env and file.
	cfg, err = LoadWithOverrides(cwd, envFunc(map[string]string{
		"HOME":                home,
		"ARGENT_PRIVACY_MODE": PrivacyModeAny,
	}), map[string]any{"privacyMode": PrivacyModeLocalOnly})
	if err != nil {
		t.Fatalf("LoadWithOverrides: %v", err)
	}
	if cfg.PrivacyMode != PrivacyModeLocalOnly {
		t.Fatalf("override PrivacyMode = %q, want %q", cfg.PrivacyMode, PrivacyModeLocalOnly)
	}
}

// TestPrivacyModeInvalidRejected documents the fail-closed choice: an
// unrecognized value fails config load instead of silently widening to "any".
func TestPrivacyModeInvalidRejected(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	writeJSONFile(t, filepath.Join(cwd, "argent.json"), map[string]any{
		"privacyMode": "locl-only",
	})
	if _, err := Load(cwd, envFunc(map[string]string{"HOME": home})); err == nil {
		t.Fatal("Load accepted an invalid file privacyMode")
	} else if !strings.Contains(err.Error(), "privacyMode") {
		t.Fatalf("error does not name the setting: %v", err)
	}

	if _, err := LoadWithOverrides(t.TempDir(), envFunc(map[string]string{"HOME": home}),
		map[string]any{"privacyMode": "bogus"}); err == nil {
		t.Fatal("LoadWithOverrides accepted an invalid privacyMode override")
	}

	if _, err := Load(t.TempDir(), envFunc(map[string]string{
		"HOME":                home,
		"ARGENT_PRIVACY_MODE": "remote",
	})); err == nil {
		t.Fatal("Load accepted an invalid ARGENT_PRIVACY_MODE")
	}
}

// TestPrivacyModeBlankUsesDefault pins that an explicitly empty setting is not
// an error: it falls back to the compiled "any" posture.
func TestPrivacyModeBlankUsesDefault(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	writeJSONFile(t, filepath.Join(cwd, "argent.json"), map[string]any{
		"privacyMode": "   ",
	})
	cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PrivacyMode != PrivacyModeAny {
		t.Fatalf("PrivacyMode = %q, want %q", cfg.PrivacyMode, PrivacyModeAny)
	}
}
