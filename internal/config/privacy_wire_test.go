package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestPrivacyModeDefaultAny 固定编译期默认值：没有文件、env 或
// flag 设置时，姿态是历史上宽松的 "any"。
func TestPrivacyModeDefaultAny(t *testing.T) {
	cfg, err := Load(t.TempDir(), envFunc(map[string]string{"HOME": t.TempDir()}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PrivacyMode != PrivacyModeAny {
		t.Fatalf("PrivacyMode = %q, want %q", cfg.PrivacyMode, PrivacyModeAny)
	}
}

// TestPrivacyModeFromConfigFile 覆盖两种文档记录的拼写。值会被
// 规范化（去空白、转小写）以精确匹配 security 枚举。
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

// TestPrivacyModePrecedence 固定新设置的 file < env < flag 优先级。
func TestPrivacyModePrecedence(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	writeJSONFile(t, filepath.Join(cwd, "argent.json"), map[string]any{
		"privacyMode": PrivacyModeLocalOnly,
	})
	baseEnv := envFunc(map[string]string{"HOME": home})

	// 文件胜过默认值。
	cfg, err := Load(cwd, baseEnv)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PrivacyMode != PrivacyModeLocalOnly {
		t.Fatalf("file PrivacyMode = %q, want %q", cfg.PrivacyMode, PrivacyModeLocalOnly)
	}

	// ARGENT_PRIVACY_MODE 胜过文件。
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

	// flag/覆盖同时胜过 env 和文件。
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

// TestPrivacyModeInvalidRejected 记录 fail-closed 选择：
// 无法识别的值会让配置加载失败，而不是悄悄放宽为 "any"。
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

// TestPrivacyModeBlankUsesDefault 固定：显式空设置不是
// 错误，它会回退到编译期的 "any" 姿态。
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
