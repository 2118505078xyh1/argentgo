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
	// Validate 为现有调用方保持"首个错误"约定。
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
	// 冲突仅供参考：配置仍然可用。
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate rejected conflicting aliases: %v", err)
	}
}

// Diagnostics 必须指出 decode 实际使用的别名。decode() 会跳过
// 类型错误的规范键并回退到下一个别名，因此把
// present[0] 报告为胜出者会指向一个从未进入 Config 的值。
func TestDiagnosticsConflictNamesValueActuallyUsed(t *testing.T) {
	cases := []struct {
		name           string
		overrides      map[string]any
		wantModel      string
		wantConflict   bool
		wantUsing      string
		wantInvalidKey string
	}{
		{
			name:      "canonical model ill-typed falls through to modelName",
			overrides: map[string]any{"model": 42, "modelName": "real-model"},
			wantModel: "real-model", wantConflict: true, wantUsing: "modelName",
			wantInvalidKey: "$.model",
		},
		{
			name:      "canonical model valid wins over conflicting alias",
			overrides: map[string]any{"model": "canonical", "modelName": "alias"},
			wantModel: "canonical", wantConflict: true, wantUsing: "model",
		},
		{
			name:         "canonical maxTurns ill-typed falls through to max_turns",
			overrides:    map[string]any{"maxTurns": "oops", "max_turns": 7},
			wantConflict: true, wantUsing: "max_turns", wantInvalidKey: "$.maxTurns",
		},
		{
			name:         "canonical maxTurns valid numeric string wins",
			overrides:    map[string]any{"maxTurns": "7", "max_turns": 9},
			wantConflict: true, wantUsing: "maxTurns",
		},
		{
			name:      "equal aliases report no conflict",
			overrides: map[string]any{"model": "same", "modelName": "same"},
			wantModel: "same", wantConflict: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := LoadWithOverrides(t.TempDir(),
				envFunc(map[string]string{"HOME": t.TempDir()}), tc.overrides)
			if err != nil {
				t.Fatalf("LoadWithOverrides: %v", err)
			}
			if tc.wantModel != "" && cfg.Model != tc.wantModel {
				t.Errorf("model = %q, want %q", cfg.Model, tc.wantModel)
			}

			var conflict *Diagnostic
			invalidKeys := map[string]bool{}
			for _, d := range cfg.Diagnostics() {
				if d.Kind == DiagnosticConflict {
					copy := d
					conflict = &copy
				}
				if d.Kind == DiagnosticInvalid {
					invalidKeys[d.Path] = true
				}
			}
			if tc.wantInvalidKey != "" && !invalidKeys[tc.wantInvalidKey] {
				t.Errorf("missing invalid diagnostic for %q: %+v", tc.wantInvalidKey, cfg.Diagnostics())
			}
			if !tc.wantConflict {
				if conflict != nil {
					t.Errorf("unexpected conflict diagnostic: %+v", conflict)
				}
				return
			}
			if conflict == nil {
				t.Fatalf("no conflict diagnostic: %+v", cfg.Diagnostics())
			}
			if !strings.Contains(conflict.Message, "using "+tc.wantUsing) {
				t.Errorf("conflict message = %q, want it to name the used key %q",
					conflict.Message, tc.wantUsing)
			}
		})
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
