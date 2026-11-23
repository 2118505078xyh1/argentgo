package config

import (
	"path/filepath"
	"testing"
)

// TestStorageConfigIsRead verifies that the file CLI login/config set writes to
// (<storage>/config.json) is actually consumed by the engine. Previously only
// <home>/.argentgo/config.json and project files were loaded, so setting
// ARGENT_STORAGE_DIR made every configured key silently ineffective.
func TestStorageConfigIsRead(t *testing.T) {
	home := t.TempDir()
	storage := t.TempDir()
	cwd := t.TempDir()

	writeJSONFile(t, filepath.Join(home, appDir, "config.json"), map[string]any{
		"model":  "home-model",
		"apiKey": "home-key",
	})
	writeJSONFile(t, filepath.Join(storage, "config.json"), map[string]any{
		"model":   "storage-model",
		"baseURL": "https://storage.example/v1",
	})
	writeJSONFile(t, filepath.Join(cwd, "argent.json"), map[string]any{
		"locale": "zh-CN",
	})

	env := envFunc(map[string]string{
		"HOME":               home,
		"ARGENT_STORAGE_DIR": storage,
	})
	cfg, err := Load(cwd, env)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// 用户写入 storage 的模型胜过 home 全局层；项目文件只改 locale，不触碰模型。
	if cfg.Model != "storage-model" {
		t.Errorf("model = %q, want storage-model", cfg.Model)
	}
	if cfg.BaseURL != "https://storage.example/v1" {
		t.Errorf("baseURL = %q, want storage endpoint", cfg.BaseURL)
	}
	if cfg.Locale != "zh-CN" {
		t.Errorf("locale = %q, want project value", cfg.Locale)
	}
	if cfg.StorageDir != storage {
		t.Errorf("storageDir = %q, want %q", cfg.StorageDir, storage)
	}

	// storage 文件是用户全局层，不能把用户自己的 baseURL 误标成项目注入。
	if cfg.BaseURLFromProject() {
		t.Error("storage baseURL wrongly reported as a project override")
	}
	wantSources := []string{
		filepath.Join(home, appDir, "config.json"),
		filepath.Join(storage, "config.json"),
		filepath.Join(cwd, "argent.json"),
	}
	if len(cfg.Sources) != len(wantSources) {
		t.Fatalf("sources = %v, want %v", cfg.Sources, wantSources)
	}
	for i, want := range wantSources {
		if cfg.Sources[i] != want {
			t.Errorf("sources[%d] = %q, want %q", i, cfg.Sources[i], want)
		}
	}
}

// TestStorageConfigPrecedence 锁定 storage 层的位置：胜过 home 全局层，
// 但低于项目文件、环境变量与 flag 覆盖。
func TestStorageConfigPrecedence(t *testing.T) {
	home := t.TempDir()
	storage := t.TempDir()
	cwd := t.TempDir()
	writeJSONFile(t, filepath.Join(home, appDir, "config.json"), map[string]any{"model": "home"})
	writeJSONFile(t, filepath.Join(storage, "config.json"), map[string]any{
		"model":    "storage",
		"maxTurns": 7,
		"yolo":     false,
	})
	writeJSONFile(t, filepath.Join(cwd, "argent.json"), map[string]any{"model": "project"})

	values := map[string]string{
		"HOME":               home,
		"ARGENT_STORAGE_DIR": storage,
	}
	env := envFunc(values)
	cfg, err := Load(cwd, env)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Model != "project" {
		t.Errorf("model = %q, want project to beat storage", cfg.Model)
	}
	if cfg.MaxTurns != 7 {
		t.Errorf("maxTurns = %d, want storage value", cfg.MaxTurns)
	}

	values["ARGENT_MODEL"] = "env"
	cfg, err = Load(cwd, env)
	if err != nil {
		t.Fatalf("Load with env: %v", err)
	}
	if cfg.Model != "env" {
		t.Errorf("model = %q, want env to beat files", cfg.Model)
	}

	flags, err := LoadWithOverrides(cwd, env, map[string]any{"model": "flag", "maxTurns": 2})
	if err != nil {
		t.Fatalf("LoadWithOverrides: %v", err)
	}
	if flags.Model != "flag" || flags.MaxTurns != 2 {
		t.Errorf("flags not applied: model=%q maxTurns=%d", flags.Model, flags.MaxTurns)
	}
}

// TestCandidatePathsIncludeStorageConfig 保证 CLI 能据 CandidatePaths 判断自己的
// 写入路径是否命中引擎候选层；未设置存储变量时 home 与 storage 重合，不重复列出。
func TestCandidatePathsIncludeStorageConfig(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	storage := t.TempDir()

	defaults := CandidatePaths(cwd, envFunc(map[string]string{"HOME": home}))
	if len(defaults) != 3 {
		t.Fatalf("default candidates = %v, want 3 (deduped)", defaults)
	}

	for _, key := range []string{"ARGENT_STORAGE", "ARGENT_STORAGE_DIR", "ARGENT_HOME"} {
		paths := CandidatePaths(cwd, envFunc(map[string]string{"HOME": home, key: storage}))
		if len(paths) != 4 || paths[1] != filepath.Join(storage, "config.json") {
			t.Errorf("%s candidates = %v, want storage config at index 1", key, paths)
		}
	}
}
