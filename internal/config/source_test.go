package config

import (
	"path/filepath"
	"testing"
)

// 来源追踪是 P0 外泄守卫的事实来源：只有项目层写下的 baseURL 才应被
// app.Resolve 拦下。这里逐层锁定它的归属，避免重构时悄悄放宽。
func TestBaseURLSourceTracksOwningLayer(t *testing.T) {
	t.Run("global config", func(t *testing.T) {
		home, cwd := t.TempDir(), t.TempDir()
		writeJSONFile(t, filepath.Join(home, appDir, "config.json"), map[string]any{
			"baseURL": "https://global.test/v1",
		})
		cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home}))
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.BaseURLSource != ValueSourceGlobal {
			t.Fatalf("source = %q, want global", cfg.BaseURLSource)
		}
		if cfg.BaseURLFromProject() {
			t.Error("global baseURL must not be reported as project-sourced")
		}
	})

	for _, name := range []string{".argent/config.json", "argent.json"} {
		t.Run("project "+name, func(t *testing.T) {
			home, cwd := t.TempDir(), t.TempDir()
			writeJSONFile(t, filepath.Join(cwd, filepath.FromSlash(name)), map[string]any{
				"baseURL": "https://project.test/v1",
			})
			cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home}))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.BaseURLSource != ValueSourceProject || !cfg.BaseURLFromProject() {
				t.Fatalf("source = %q, want project", cfg.BaseURLSource)
			}
		})
	}

	t.Run("project beats global", func(t *testing.T) {
		home, cwd := t.TempDir(), t.TempDir()
		writeJSONFile(t, filepath.Join(home, appDir, "config.json"), map[string]any{
			"baseURL": "https://global.test/v1",
		})
		writeJSONFile(t, filepath.Join(cwd, "argent.json"), map[string]any{
			"baseURL": "https://project.test/v1",
		})
		cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home}))
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.BaseURLSource != ValueSourceProject {
			t.Fatalf("source = %q, want project (last writer wins)", cfg.BaseURLSource)
		}
	})

	t.Run("env beats project", func(t *testing.T) {
		home, cwd := t.TempDir(), t.TempDir()
		writeJSONFile(t, filepath.Join(cwd, "argent.json"), map[string]any{
			"baseURL": "https://project.test/v1",
		})
		cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home, "ARGENT_BASE_URL": "https://env.test/v1"}))
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.BaseURLSource != ValueSourceEnv || cfg.BaseURLFromProject() {
			t.Fatalf("source = %q, want env", cfg.BaseURLSource)
		}
	})

	t.Run("override beats project", func(t *testing.T) {
		home, cwd := t.TempDir(), t.TempDir()
		writeJSONFile(t, filepath.Join(cwd, "argent.json"), map[string]any{
			"baseURL": "https://project.test/v1",
		})
		cfg, err := LoadWithOverrides(cwd, envFunc(map[string]string{"HOME": home}),
			map[string]any{"baseURL": "https://flag.test/v1"})
		if err != nil {
			t.Fatalf("LoadWithOverrides: %v", err)
		}
		if cfg.BaseURLSource != ValueSourceOverride || cfg.BaseURLFromProject() {
			t.Fatalf("source = %q, want override", cfg.BaseURLSource)
		}
	})

	t.Run("unset keeps zero source", func(t *testing.T) {
		cfg, err := Load(t.TempDir(), envFunc(map[string]string{"HOME": t.TempDir()}))
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.BaseURLSource != ValueSourceUnset {
			t.Fatalf("source = %q, want unset", cfg.BaseURLSource)
		}
	})
}

// 项目文件里定义的 profile 改写端点时，来源也必须归到项目层，
// 否则一条 profile 覆盖就能绕过信任检查。
func TestBaseURLSourceFromProjectProfile(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	writeJSONFile(t, filepath.Join(cwd, "argent.json"), map[string]any{
		"profile":  "work",
		"profiles": map[string]any{"work": map[string]any{"baseURL": "https://profile-project.test/v1"}},
	})
	cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.BaseURLFromProject() {
		t.Fatalf("profile baseURL source = %q, want project", cfg.BaseURLSource)
	}
}

func TestProviderSourceTracksOwningLayer(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	writeJSONFile(t, filepath.Join(cwd, "argent.json"), map[string]any{
		"providers": map[string]any{"OpenAI": map[string]any{
			"baseURL": "https://provider-project.test/v1",
			"apiKey":  "sk-project",
		}},
	})
	cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.ProviderBaseURLSource("openai"); got != ValueSourceProject {
		t.Errorf("provider baseURL source = %q, want project", got)
	}
	if got := cfg.ProviderAPIKeySource("openai"); got != ValueSourceProject {
		t.Errorf("provider apiKey source = %q, want project", got)
	}
	if got := cfg.ProviderBaseURLSource("does-not-exist"); got != ValueSourceUnset {
		t.Errorf("unknown provider source = %q, want unset", got)
	}
}

func TestAPIKeySourceTracksOwningLayer(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	writeJSONFile(t, filepath.Join(cwd, "argent.json"), map[string]any{"apiKey": "sk-project"})
	cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.APIKeySource != ValueSourceProject {
		t.Fatalf("apiKey source = %q, want project", cfg.APIKeySource)
	}

	cfg, err = Load(cwd, envFunc(map[string]string{"HOME": home, "ARGENT_API_KEY": "sk-env"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.APIKeySource != ValueSourceEnv {
		t.Fatalf("apiKey source = %q, want env", cfg.APIKeySource)
	}
}
