package config

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// failClosedValidConfig is a hand-built Config that satisfies every invariant
// the fail-closed summary checks. It deliberately leaves all other fields at
// their zero values: Diagnostics only inspects the settings under test here.
func failClosedValidConfig() Config {
	return Config{
		Model:                      "test-model",
		SandboxMode:                SandboxWorkspaceWrite,
		PrivacyMode:                PrivacyModeAny,
		MaxTurns:                   12,
		VerifyTimeoutMS:            30000,
		VerifyMaxReflections:       0,
		ContextWindow:              0,
		AutoCondenseContextPercent: 0,
	}
}

func TestFailClosedDiagnosticsTable(t *testing.T) {
	cases := []struct {
		name      string
		mutate    func(*Config)
		wantPaths []string
	}{
		{"valid config", func(*Config) {}, nil},
		{
			name:      "unknown privacy mode",
			mutate:    func(c *Config) { c.PrivacyMode = "remote" },
			wantPaths: []string{"$.privacyMode"},
		},
		{
			// A hand-built Config has an empty PrivacyMode (Load always
			// canonicalizes it to "any"). The summary must still fail closed.
			name:      "empty privacy mode",
			mutate:    func(c *Config) { c.PrivacyMode = "" },
			wantPaths: []string{"$.privacyMode"},
		},
		{
			name:      "condense percent above 100",
			mutate:    func(c *Config) { c.AutoCondenseContextPercent = 101 },
			wantPaths: []string{"$.autoCondenseContextPercent"},
		},
		{
			name:      "condense percent below -1",
			mutate:    func(c *Config) { c.AutoCondenseContextPercent = -2 },
			wantPaths: []string{"$.autoCondenseContextPercent"},
		},
		{
			name:   "condense percent -1 inherits",
			mutate: func(c *Config) { c.AutoCondenseContextPercent = -1 },
		},
		{
			name:   "condense percent 100 boundary",
			mutate: func(c *Config) { c.AutoCondenseContextPercent = 100 },
		},
		{
			name:      "negative context window",
			mutate:    func(c *Config) { c.ContextWindow = -1 },
			wantPaths: []string{"$.contextWindow"},
		},
		{
			name:      "negative verify timeout",
			mutate:    func(c *Config) { c.VerifyTimeoutMS = -1 },
			wantPaths: []string{"$.verifyTimeoutMS"},
		},
		{
			name:      "negative verify reflections",
			mutate:    func(c *Config) { c.VerifyMaxReflections = -1 },
			wantPaths: []string{"$.verifyMaxReflections"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := failClosedValidConfig()
			tc.mutate(&cfg)

			var got []string
			for _, d := range cfg.Diagnostics() {
				if d.Kind != DiagnosticInvalid {
					t.Errorf("unexpected non-invalid diagnostic: %+v", d)
					continue
				}
				got = append(got, d.Path)
			}
			if !reflect.DeepEqual(got, tc.wantPaths) {
				t.Errorf("diagnostic paths = %v, want %v (all diagnostics: %+v)",
					got, tc.wantPaths, cfg.Diagnostics())
			}

			err := cfg.Validate()
			if len(tc.wantPaths) == 0 {
				if err != nil {
					t.Errorf("Validate rejected a valid config: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate accepted an invalid config (wanted %v)", tc.wantPaths)
			}
			field := strings.TrimPrefix(tc.wantPaths[0], "$.")
			if !strings.Contains(err.Error(), field) {
				t.Errorf("Validate error = %q, want it to mention %q", err, field)
			}
		})
	}
}

// The summary must not stop at the first fail-closed problem: all issues are
// reported together, in a stable order, so doctor can print them in one pass.
func TestFailClosedDiagnosticsCollectsAllIssues(t *testing.T) {
	cfg := failClosedValidConfig()
	cfg.PrivacyMode = "bogus"
	cfg.AutoCondenseContextPercent = 200
	cfg.ContextWindow = -8
	cfg.VerifyTimeoutMS = -3
	cfg.VerifyMaxReflections = -2

	want := []string{
		"$.privacyMode",
		"$.autoCondenseContextPercent",
		"$.contextWindow",
		"$.verifyTimeoutMS",
		"$.verifyMaxReflections",
	}
	var got []string
	for _, d := range cfg.Diagnostics() {
		got = append(got, d.Path)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("diagnostic paths = %v, want %v", got, want)
	}
	// The legacy checks still run first and still own Validate's first error.
	cfg.SandboxMode = "loose"
	if first := cfg.Diagnostics()[0]; first.Path != "$.sandboxMode" {
		t.Errorf("first diagnostic = %+v, want $.sandboxMode", first)
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "sandboxMode") {
		t.Errorf("Validate error = %v, want the legacy sandboxMode complaint", err)
	}
}

// The fail-closed summary is enforced at load time, not just when a caller
// explicitly asks for diagnostics. This is the "effective" part: an unsafe
// config cannot reach the agent loop through the normal startup path.
func TestLoadFailsClosedOnSummaryIssues(t *testing.T) {
	cases := []struct {
		name string
		file map[string]any
		want string
	}{
		{"privacy mode", map[string]any{"privacyMode": "remote"}, "privacyMode"},
		{"percent out of range", map[string]any{"autoCondenseContextPercent": 120}, "autoCondenseContextPercent"},
		{"negative verify timeout", map[string]any{"verifyTimeoutMS": -1}, "verifyTimeoutMS"},
		{"negative verify reflections", map[string]any{"verifyMaxReflections": -1}, "verifyMaxReflections"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cwd := t.TempDir()
			writeJSONFile(t, filepath.Join(cwd, "argent.json"), tc.file)
			_, err := Load(cwd, envFunc(map[string]string{"HOME": t.TempDir()}))
			if err == nil {
				t.Fatal("Load accepted an unsafe config")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Load error = %q, want it to mention %q", err, tc.want)
			}
		})
	}

	// A config that only uses the legacy value-level failures still loads;
	// those remain an explicit Validate() contract, not a load-time one.
	cfg, err := LoadWithOverrides(t.TempDir(),
		envFunc(map[string]string{"HOME": t.TempDir()}),
		map[string]any{"sandboxMode": "loose"})
	if err != nil {
		t.Fatalf("LoadWithOverrides: %v", err)
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate accepted a bad sandboxMode")
	}
}
