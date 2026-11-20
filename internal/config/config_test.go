package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func envFunc(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func writeJSONFile(t *testing.T, path string, value any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestLoadPrecedence(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	writeJSONFile(t, filepath.Join(home, appDir, "config.json"), map[string]any{
		"model":    "home-model",
		"locale":   "zh-CN",
		"maxTurns": 5,
	})
	writeJSONFile(t, filepath.Join(cwd, ".argent", "config.json"), map[string]any{
		"model":      "project-model",
		"storageDir": filepath.Join(cwd, "store"),
	})
	writeJSONFile(t, filepath.Join(cwd, "argent.json"), map[string]any{
		"maxTurns": 9,
	})

	env := envFunc(map[string]string{
		"HOME":           home,
		"ARGENT_LOCALE":  "en-US",
		"ARGENT_YOLO":    "true",
		"ARGENT_API_KEY": "env-key",
	})
	cfg, err := Load(cwd, env)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Model != "project-model" {
		t.Errorf("model = %q, want project-model (project beats home)", cfg.Model)
	}
	if cfg.Locale != "en-US" {
		t.Errorf("locale = %q, want en-US (env beats files)", cfg.Locale)
	}
	if cfg.MaxTurns != 9 {
		t.Errorf("maxTurns = %d, want 9 (argent.json beats .argent/config.json)", cfg.MaxTurns)
	}
	if cfg.StorageDir != filepath.Join(cwd, "store") {
		t.Errorf("storageDir = %q", cfg.StorageDir)
	}
	if !cfg.Yolo {
		t.Error("yolo = false, want true from ARGENT_YOLO")
	}
	if cfg.APIKey != "env-key" {
		t.Errorf("apiKey = %q, want env-key", cfg.APIKey)
	}
	if len(cfg.Sources) != 3 {
		t.Errorf("sources = %v, want 3 files", cfg.Sources)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	flags, err := LoadWithOverrides(cwd, env, map[string]any{
		"model":    "flag-model",
		"maxTurns": 2,
	})
	if err != nil {
		t.Fatalf("LoadWithOverrides: %v", err)
	}
	if flags.Model != "flag-model" || flags.MaxTurns != 2 {
		t.Errorf("flags not applied: model=%q maxTurns=%d", flags.Model, flags.MaxTurns)
	}
}

func TestDefaultsWithoutFiles(t *testing.T) {
	home := t.TempDir()
	cfg, err := Load(t.TempDir(), envFunc(map[string]string{"HOME": home}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Model != defaultModel {
		t.Errorf("model = %q, want %q", cfg.Model, defaultModel)
	}
	if cfg.SandboxMode != SandboxWorkspaceWrite {
		t.Errorf("sandboxMode = %q, want %q", cfg.SandboxMode, SandboxWorkspaceWrite)
	}
	if cfg.MaxTurns != defaultMaxTurns {
		t.Errorf("maxTurns = %d, want %d", cfg.MaxTurns, defaultMaxTurns)
	}
	if cfg.StorageDir != filepath.Join(home, appDir) {
		t.Errorf("storageDir = %q", cfg.StorageDir)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("defaults do not validate: %v", err)
	}
}

func TestProfileMergesOverBase(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	writeJSONFile(t, filepath.Join(home, appDir, "config.json"), map[string]any{
		"model": "base-model",
		"profiles": map[string]any{
			"fast": map[string]any{"model": "fast-model", "maxTurns": 3},
			"slow": map[string]any{"model": "slow-model"},
		},
	})

	cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home, "ARGENT_PROFILE": "fast"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Model != "fast-model" || cfg.MaxTurns != 3 {
		t.Errorf("profile not merged: model=%q maxTurns=%d", cfg.Model, cfg.MaxTurns)
	}
	if cfg.ActiveProfile != "fast" {
		t.Errorf("activeProfile = %q, want fast", cfg.ActiveProfile)
	}

	// Env wins over the profile.
	cfg, err = Load(cwd, envFunc(map[string]string{
		"HOME":           home,
		"ARGENT_PROFILE": "fast",
		"ARGENT_MODEL":   "env-model",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Model != "env-model" {
		t.Errorf("model = %q, want env-model", cfg.Model)
	}
}

func TestProfileChosenFromFile(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	writeJSONFile(t, filepath.Join(cwd, "argent.json"), map[string]any{
		"profile": "reviewer",
		"profiles": map[string]any{
			"reviewer": map[string]any{"model": "review-model", "sandboxMode": SandboxReadOnly},
		},
	})
	cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Model != "review-model" || cfg.SandboxMode != SandboxReadOnly {
		t.Errorf("file profile not merged: %+v", cfg)
	}
	if cfg.ActiveProfile != "reviewer" {
		t.Errorf("activeProfile = %q", cfg.ActiveProfile)
	}
}

func TestUnknownProfileIsAnError(t *testing.T) {
	home := t.TempDir()
	writeJSONFile(t, filepath.Join(home, appDir, "config.json"), map[string]any{
		"profiles": map[string]any{"fast": map[string]any{"model": "m"}},
	})
	if _, err := Load(t.TempDir(), envFunc(map[string]string{
		"HOME":           home,
		"ARGENT_PROFILE": "missing",
	})); err == nil {
		t.Fatal("unknown profile was accepted")
	}
}

func TestUnknownKeysAreTolerated(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	writeJSONFile(t, filepath.Join(cwd, "argent.json"), map[string]any{
		"model":       "kept-model",
		"futureThing": map[string]any{"nested": true},
	})
	cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Model != "kept-model" {
		t.Errorf("model = %q", cfg.Model)
	}
	future, ok := cfg.Raw["futureThing"].(map[string]any)
	if !ok || future["nested"] != true {
		t.Errorf("unknown key was dropped from Raw: %#v", cfg.Raw["futureThing"])
	}
}

func TestMalformedConfigIsReported(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	path := filepath.Join(cwd, ".argent", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(cwd, envFunc(map[string]string{"HOME": home})); err == nil {
		t.Fatal("malformed config was accepted")
	}
}

func TestValidate(t *testing.T) {
	home := t.TempDir()
	env := envFunc(map[string]string{"HOME": home})
	cases := []struct {
		name      string
		overrides map[string]any
		want      string
	}{
		{"bad sandbox", map[string]any{"sandboxMode": "loose"}, "sandboxMode"},
		{"empty model", map[string]any{"model": ""}, "model"},
		{"negative turns", map[string]any{"maxTurns": -1}, "maxTurns"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			cfg, err := LoadWithOverrides(t.TempDir(), env, testCase.overrides)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			err = cfg.Validate()
			if err == nil {
				t.Fatalf("Validate accepted %+v", cfg)
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Errorf("error = %q, want it to mention %q", err, testCase.want)
			}
		})
	}

	cfg, err := Load(t.TempDir(), env)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

func TestProviderResolution(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	env := envFunc(map[string]string{"HOME": home, "ANTHROPIC_API_KEY": "secret"})

	cfg, err := Load(cwd, env)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	anthropic, ok := cfg.Provider("anthropic")
	if !ok {
		t.Fatal("anthropic fallback missing")
	}
	if anthropic.Protocol != "anthropic-messages" || anthropic.APIKeyEnv != "ANTHROPIC_API_KEY" {
		t.Errorf("anthropic = %+v", anthropic)
	}
	if !strings.Contains(anthropic.BaseURL, "api.anthropic.com") || anthropic.DefaultModel == "" {
		t.Errorf("anthropic fallback = %+v", anthropic)
	}

	ollama, ok := cfg.Provider("ollama")
	if !ok || !strings.Contains(ollama.BaseURL, "11434") {
		t.Errorf("ollama = %+v, ok=%v", ollama, ok)
	}

	// An empty name follows the protocol, which defaults to OpenAI.
	openai, ok := cfg.Provider("")
	if !ok || openai.Protocol != "openai-chat-completions" {
		t.Errorf("protocol fallback = %+v, ok=%v", openai, ok)
	}
	if _, ok := cfg.Provider("does-not-exist"); ok {
		t.Error("unknown provider resolved")
	}

	// Config entries merge over the built-in instead of replacing it.
	writeJSONFile(t, filepath.Join(cwd, "argent.json"), map[string]any{
		"provider": "anthropic",
		"providers": map[string]any{
			"anthropic": map[string]any{
				"baseURL":      "http://example.test/v1",
				"defaultModel": "claude-local",
			},
		},
	})
	cfg, err = Load(cwd, env)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	custom, ok := cfg.Provider("")
	if !ok {
		t.Fatal("configured provider missing")
	}
	if custom.BaseURL != "http://example.test/v1" || custom.DefaultModel != "claude-local" {
		t.Errorf("custom provider not applied: %+v", custom)
	}
	if custom.Protocol != "anthropic-messages" || custom.APIKeyEnv != "ANTHROPIC_API_KEY" {
		t.Errorf("custom provider did not inherit built-in fields: %+v", custom)
	}
	// Load must not fold the provider env key into the top-level apiKey: that
	// would outrank the `argent login` key in app.Resolve. The env fallback now
	// sits at the end of app's credential chain instead.
	if cfg.APIKey != "" {
		t.Errorf("apiKey = %q, want empty (provider env must not pollute the top level)", cfg.APIKey)
	}

	// ARGENT_PROVIDER selects the default provider name.
	cfg, err = Load(cwd, envFunc(map[string]string{
		"HOME":            home,
		"ARGENT_PROVIDER": "ollama",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	selected, ok := cfg.Provider("")
	if !ok || selected.Name != "ollama" {
		t.Errorf("ARGENT_PROVIDER not applied: %+v ok=%v", selected, ok)
	}
}

func TestEnvValidationErrors(t *testing.T) {
	home := t.TempDir()
	for name, value := range map[string]string{
		"ARGENT_MAX_TURNS": "many",
		"ARGENT_YOLO":      "perhaps",
	} {
		if _, err := Load(t.TempDir(), envFunc(map[string]string{"HOME": home, name: value})); err == nil {
			t.Errorf("%s=%q was accepted", name, value)
		}
	}
}

// PowerShell 5.x saves "UTF-8 with BOM"; the BOM must not reach json.Unmarshal
// (it reports `invalid character 'ï'`), and a BOM-only file is just empty.
func TestLoadStripsUTF8BOM(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	path := filepath.Join(cwd, "argent.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "{\n  // a comment to prove JSONC cleaning still runs\n  \"model\": \"bom-model\",\n}\n"
	if err := os.WriteFile(path, append([]byte("\ufeff"), []byte(body)...), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home}))
	if err != nil {
		t.Fatalf("Load with BOM: %v", err)
	}
	if cfg.Model != "bom-model" {
		t.Errorf("model = %q, want bom-model", cfg.Model)
	}
}

func TestLoadBOMOnlyFileIsIgnored(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	path := filepath.Join(cwd, "argent.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("\ufeff"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home}))
	if err != nil {
		t.Fatalf("Load with BOM-only file: %v", err)
	}
	if len(cfg.Sources) != 0 {
		t.Errorf("sources = %v, want none (BOM-only file must be treated as empty)", cfg.Sources)
	}
}

// A provider's API-key env var must stay at the bottom of the credential
// chain (app.Resolve) instead of being copied into the top-level apiKey,
// where it would outrank the `argent login` key under providers.<name>.apiKey.
func TestLoadDoesNotPolluteAPIKeyFromProviderEnv(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	writeJSONFile(t, filepath.Join(cwd, "argent.json"), map[string]any{
		"provider": "openai",
		"providers": map[string]any{
			"openai": map[string]any{"apiKey": "sk-login"},
		},
	})
	cfg, err := Load(cwd, envFunc(map[string]string{
		"HOME":           home,
		"OPENAI_API_KEY": "sk-openai-env",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.APIKey != "" {
		t.Errorf("apiKey = %q, want empty (provider env must not become a top-level config key)", cfg.APIKey)
	}
	provider, ok := cfg.Provider("openai")
	if !ok || provider.APIKey != "sk-login" {
		t.Errorf("login key lost: provider = %+v, ok = %v", provider, ok)
	}
}
