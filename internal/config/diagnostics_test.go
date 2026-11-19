package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDiagnosticsReportsAllProblemsAtOnce(t *testing.T) {
	cfg, err := LoadWithOverrides(t.TempDir(),
		envFunc(map[string]string{"HOME": t.TempDir()}),
		map[string]any{"sandboxMode": "loose", "model": "", "maxTurns": -1})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	diagnostics := cfg.Diagnostics()
	if len(diagnostics) != 3 {
		t.Fatalf("diagnostics = %+v, want 3 problems", diagnostics)
	}
	wantPath := []string{"$.sandboxMode", "$.model", "$.maxTurns"}
	for i, diagnostic := range diagnostics {
		if diagnostic.Path != wantPath[i] {
			t.Errorf("diagnostic %d path = %q, want %q", i, diagnostic.Path, wantPath[i])
		}
		if diagnostic.Kind != DiagnosticInvalid {
			t.Errorf("diagnostic %d kind = %q, want invalid", i, diagnostic.Kind)
		}
	}
	// Validate keeps its first-error contract for existing callers.
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "sandboxMode") {
		t.Errorf("Validate error = %v, want a sandboxMode complaint", err)
	}
}

func TestDiagnosticsConflictAndUnsupported(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	writeJSONFile(t, filepath.Join(cwd, "argent.json"), map[string]any{
		"baseURL":  "https://canonical.test/v1",
		"base_url": "https://alias.test/v1",
		"logLevel": "debug",
	})
	cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	var conflict, unsupported *Diagnostic
	for _, d := range cfg.Diagnostics() {
		switch d.Kind {
		case DiagnosticConflict:
			copy := d
			conflict = &copy
		case DiagnosticUnsupported:
			copy := d
			unsupported = &copy
		}
	}
	if conflict == nil || conflict.Path != "$.baseURL" {
		t.Fatalf("conflict diagnostic = %+v, want $.baseURL", conflict)
	}
	if unsupported == nil || unsupported.Path != "$.logLevel" {
		t.Fatalf("unsupported diagnostic = %+v, want $.logLevel", unsupported)
	}
	// A conflict is informational: the config remains usable.
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate rejected conflicting aliases: %v", err)
	}
}

func TestDiagnosticsEmptyForValidConfig(t *testing.T) {
	cfg, err := Load(t.TempDir(), envFunc(map[string]string{"HOME": t.TempDir()}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Diagnostics(); len(got) != 0 {
		t.Errorf("diagnostics = %+v, want none", got)
	}
}

func TestDiagnosticsInvalidTypes(t *testing.T) {
	cfg, err := LoadWithOverrides(t.TempDir(),
		envFunc(map[string]string{"HOME": t.TempDir()}),
		map[string]any{"maxTurns": "many"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	found := false
	for _, d := range cfg.Diagnostics() {
		if d.Path == "$.maxTurns" && d.Kind == DiagnosticInvalid {
			found = true
		}
	}
	if !found {
		t.Errorf("invalid maxTurns type not diagnosed: %+v", cfg.Diagnostics())
	}
}
