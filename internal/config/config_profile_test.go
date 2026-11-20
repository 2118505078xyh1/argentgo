package config

import (
	"path/filepath"
	"testing"
)

func TestContextWindowDecode(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()

	// 没有上下文窗口键：该字段保持零值。
	cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ContextWindow != 0 {
		t.Fatalf("absent contextWindow = %d, want 0", cfg.ContextWindow)
	}

	writeJSONFile(t, filepath.Join(cwd, "argent.json"), map[string]any{
		"contextWindow": 128_000,
	})
	cfg, err = Load(cwd, envFunc(map[string]string{"HOME": home}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ContextWindow != 128_000 {
		t.Fatalf("contextWindow = %d, want 128000", cfg.ContextWindow)
	}
}

func TestContextWindowFromModelAndProviderEntries(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	writeJSONFile(t, filepath.Join(cwd, "argent.json"), map[string]any{
		"model":    "my-model",
		"provider": "custom",
		"models": map[string]any{
			"my-model": map[string]any{"contextWindow": 200_000},
		},
		"providers": map[string]any{
			"custom": map[string]any{"contextWindow": 64_000},
		},
	})
	cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// model 条目胜过 provider 条目。
	if cfg.ContextWindow != 200_000 {
		t.Fatalf("model contextWindow = %d, want 200000", cfg.ContextWindow)
	}
}

func TestAutoCondenseContextPercentDecode(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	writeJSONFile(t, filepath.Join(cwd, "argent.json"), map[string]any{
		"profiles": map[string]any{
			"fast":      map[string]any{"autoCondenseContextPercent": 60},
			"inherited": map[string]any{"autoCondenseContextPercent": -1},
			"plain":     map[string]any{"model": "m"},
		},
	})

	// profile 缺失时保持零值。
	cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AutoCondenseContextPercent != 0 {
		t.Fatalf("absent percent = %d, want 0", cfg.AutoCondenseContextPercent)
	}

	// 活动 profile 的百分比被解码到该字段中。
	cfg, err = Load(cwd, envFunc(map[string]string{"HOME": home, "ARGENT_PROFILE": "fast"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AutoCondenseContextPercent != 60 {
		t.Fatalf("profile percent = %d, want 60", cfg.AutoCondenseContextPercent)
	}
	if got := cfg.CondensePercent("fast"); got != 60 {
		t.Fatalf("CondensePercent(fast) = %d, want 60", got)
	}

	// -1 被解析为显式的"继承全局"标记。
	cfg, err = Load(cwd, envFunc(map[string]string{"HOME": home, "ARGENT_PROFILE": "inherited"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AutoCondenseContextPercent != -1 {
		t.Fatalf("negative percent = %d, want -1", cfg.AutoCondenseContextPercent)
	}
	if got := cfg.CondensePercent("inherited"); got != -1 {
		t.Fatalf("CondensePercent(inherited) = %d, want -1", got)
	}

	// 没有该键的 profile 读为零，而不是另一个 profile 的值。
	cfg, err = Load(cwd, envFunc(map[string]string{"HOME": home, "ARGENT_PROFILE": "plain"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.CondensePercent("plain"); got != 0 {
		t.Fatalf("CondensePercent(plain) = %d, want 0", got)
	}

	// flag 选定的 profile 直接从 Profiles map 读取，即使
	// 它不是配置的当前活动 profile。
	if got := cfg.CondensePercent("fast"); got != 60 {
		t.Fatalf("CondensePercent(fast) = %d, want 60", got)
	}
}

func TestProfileSelectedByOverride(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	writeJSONFile(t, filepath.Join(cwd, "argent.json"), map[string]any{
		"model":   "base-model",
		"profile": "reviewer",
		"profiles": map[string]any{
			"fast":     map[string]any{"model": "fast-model", "maxTurns": 3},
			"slow":     map[string]any{"model": "slow-model"},
			"reviewer": map[string]any{"model": "review-model"},
		},
	})

	cases := []struct {
		name        string
		env         map[string]string
		overrides   map[string]any
		wantProfile string
		wantModel   string
		wantTurns   int
		wantErr     bool
	}{
		{
			name:        "-c profile selects a profile and beats the file key",
			env:         map[string]string{"HOME": home},
			overrides:   map[string]any{"profile": "fast"},
			wantProfile: "fast",
			wantModel:   "fast-model",
			wantTurns:   3,
		},
		{
			name:        "-c model still overrides the selected profile's value",
			env:         map[string]string{"HOME": home},
			overrides:   map[string]any{"profile": "fast", "model": "cli-model"},
			wantProfile: "fast",
			wantModel:   "cli-model",
			wantTurns:   3,
		},
		{
			name:        "file profile still applies alongside an unrelated -c key",
			env:         map[string]string{"HOME": home},
			overrides:   map[string]any{"maxTurns": 7},
			wantProfile: "reviewer",
			wantModel:   "review-model",
			wantTurns:   7,
		},
		{
			name:        "ARGENT_PROFILE beats -c profile",
			env:         map[string]string{"HOME": home, "ARGENT_PROFILE": "slow"},
			overrides:   map[string]any{"profile": "fast"},
			wantProfile: "slow",
			wantModel:   "slow-model",
			wantTurns:   defaultMaxTurns,
		},
		{
			name:        "env model still beats the selected profile's value",
			env:         map[string]string{"HOME": home, "ARGENT_MODEL": "env-model"},
			overrides:   map[string]any{"profile": "fast"},
			wantProfile: "fast",
			wantModel:   "env-model",
			wantTurns:   3,
		},
		{
			name:        "-c activeProfile alias selects a profile",
			env:         map[string]string{"HOME": home},
			overrides:   map[string]any{"activeProfile": "slow"},
			wantProfile: "slow",
			wantModel:   "slow-model",
			wantTurns:   defaultMaxTurns,
		},
		{
			name:      "unknown -c profile is an error",
			env:       map[string]string{"HOME": home},
			overrides: map[string]any{"profile": "missing"},
			wantErr:   true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := LoadWithOverrides(cwd, envFunc(tc.env), tc.overrides)
			if tc.wantErr {
				if err == nil {
					t.Fatal("unknown profile was accepted")
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadWithOverrides: %v", err)
			}
			if cfg.ActiveProfile != tc.wantProfile {
				t.Errorf("activeProfile = %q, want %q", cfg.ActiveProfile, tc.wantProfile)
			}
			if cfg.Model != tc.wantModel {
				t.Errorf("model = %q, want %q", cfg.Model, tc.wantModel)
			}
			if cfg.MaxTurns != tc.wantTurns {
				t.Errorf("maxTurns = %d, want %d", cfg.MaxTurns, tc.wantTurns)
			}
		})
	}
}
