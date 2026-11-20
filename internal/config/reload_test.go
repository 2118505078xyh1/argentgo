package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestChangedDetectsDiskEditAndReload 是核心 P1 场景：长生命周期
// 调用方加载一次，运维人员在磁盘上编辑配置文件，无需
// 重启，Changed() 就会翻为 true，Reload() 产生带递增
// generation 的新值。
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

	// 使用不同长度的值，使大小也发生变化；某些文件系统上
	// mtime 可能较粗。
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
	// 原始快照保持不变。
	if cfg.Model != "before" {
		t.Errorf("receiver mutated: model = %q, want before", cfg.Model)
	}
	// 重载成功后，新快照再次是干净的。
	if next.Changed() {
		t.Error("Changed() true on freshly reloaded config")
	}
}

// TestChangedNoFalsePositiveWithoutEdit 防止轮询噪声：没有磁盘活动时
// 重复调用 Changed() 必须保持 false。
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

// TestChangedDetectsNewConfigFile 覆盖此前缺失的层
// 出现（存在性变化）的情形，而不仅是对现有文件的编辑。
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

// TestReloadKeepsOverridePriority 验证 Reload 重新应用原始的
// flag 覆盖，因此文档记录的优先级在重载后不变。
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
	// flag 覆盖仍胜过被编辑的文件；对于 locale，env 仍胜过两者。
	// 只有底层（未被遮蔽的）磁盘值会改变。
	if next.Model != "flag-wins" {
		t.Errorf("override lost on reload: model = %q", next.Model)
	}
	if next.Locale != "zh-CN" {
		t.Errorf("env lost on reload: locale = %q", next.Locale)
	}
}

// TestReloadReturnErrorOnMalformedDisk 保持重载失败的非破坏性：
// 若磁盘编辑无效，调用方继续使用当前配置。
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
	// 接收者仍是最后一个已知的良好快照。
	if cfg.Model != "valid" || cfg.Generation() != 1 {
		t.Errorf("receiver damaged by failed reload: model=%q gen=%d", cfg.Model, cfg.Generation())
	}
}

// TestHandBuiltConfigHasNoChangeDetection 记录对在 Load 之外创建的
// Config 值的安全默认（多个 app 测试就是这么做的）。
func TestHandBuiltConfigHasNoChangeDetection(t *testing.T) {
	cfg := Config{Model: "manual"}
	if cfg.Changed() {
		t.Error("Changed() true for a hand-built Config")
	}
	if cfg.Generation() != 0 {
		t.Errorf("generation = %d, want 0 for a hand-built Config", cfg.Generation())
	}
}
