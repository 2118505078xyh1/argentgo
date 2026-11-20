package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// 编译期默认值不得遮蔽写在配置文件中的别名拼写。
// defaults() 过去会在基础层注入规范键（model、baseURL 等）；
// mergeTree 随后保留两种拼写，decode 先读规范
// 默认值，从而悄悄丢弃文件中的值。诊断甚至会针对这对值报告
// 虚假的 "conflicting values ... using <canonical>"。
func TestAliasedFileKeysWinOverCompiledDefaults(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	store := filepath.Join(cwd, "store")
	writeJSONFile(t, filepath.Join(cwd, "argent.json"), map[string]any{
		"modelName":    "file-model",
		"base_url":     "https://file.test/v1",
		"api_key":      "sk-file",
		"sandbox_mode": SandboxReadOnly,
		"max_turns":    4,
		"storage_dir":  store,
	})
	cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Model != "file-model" {
		t.Errorf("model = %q, want file-model", cfg.Model)
	}
	if cfg.BaseURL != "https://file.test/v1" {
		t.Errorf("baseURL = %q, want the file alias value", cfg.BaseURL)
	}
	if cfg.APIKey != "sk-file" {
		t.Errorf("apiKey = %q, want the file alias value", cfg.APIKey)
	}
	if cfg.SandboxMode != SandboxReadOnly {
		t.Errorf("sandboxMode = %q, want the file alias value", cfg.SandboxMode)
	}
	if cfg.MaxTurns != 4 {
		t.Errorf("maxTurns = %d, want the file alias value", cfg.MaxTurns)
	}
	if cfg.StorageDir != store {
		t.Errorf("storageDir = %q, want the file alias value", cfg.StorageDir)
	}
	// 单一拼写不算冲突；默认值不再是一个键。
	for _, d := range cfg.Diagnostics() {
		if d.Kind == DiagnosticConflict {
			t.Errorf("unexpected conflict diagnostic: %+v", d)
		}
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

// 两种可接受的拼写对每个带别名的字段仍然有效，
// 且规范拼写在同一层内保持文档记录的优先级。
func TestAliasedDefaultsAcceptBothSpellings(t *testing.T) {
	cases := []struct {
		name      string
		file      map[string]any
		wantModel string
		wantBase  string
		wantKey   string
		wantSbx   string
		wantTurns int
		wantStore string
		wantDiags int // conflicts expected when both spellings are present
	}{
		{
			name:      "canonical spellings",
			file:      map[string]any{"model": "m", "baseURL": "u", "apiKey": "k", "sandboxMode": SandboxReadOnly, "maxTurns": 3, "storageDir": "s"},
			wantModel: "m", wantBase: "u", wantKey: "k", wantSbx: SandboxReadOnly, wantTurns: 3, wantStore: "s",
		},
		{
			name:      "snake_case and short aliases",
			file:      map[string]any{"modelName": "m", "base_url": "u", "api_key": "k", "sandbox_mode": SandboxReadOnly, "max_turns": 3, "storage_dir": "s"},
			wantModel: "m", wantBase: "u", wantKey: "k", wantSbx: SandboxReadOnly, wantTurns: 3, wantStore: "s",
		},
		{
			name:      "baseUrl middle spelling",
			file:      map[string]any{"baseUrl": "u"},
			wantBase:  "u",
			wantModel: defaultModel, wantSbx: SandboxWorkspaceWrite, wantTurns: defaultMaxTurns,
		},
		{
			name:      "both spellings in one file: canonical wins and conflict is reported",
			file:      map[string]any{"model": "canonical", "modelName": "alias"},
			wantModel: "canonical", wantBase: defaultBaseURL, wantSbx: SandboxWorkspaceWrite,
			wantTurns: defaultMaxTurns, wantDiags: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home, cwd := t.TempDir(), t.TempDir()
			writeJSONFile(t, filepath.Join(cwd, "argent.json"), tc.file)
			cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home}))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			wantStore := tc.wantStore
			if wantStore == "" {
				wantStore = filepath.Join(home, appDir)
			}
			if cfg.Model != tc.wantModel || cfg.BaseURL != tc.wantBase || cfg.APIKey != tc.wantKey ||
				cfg.SandboxMode != tc.wantSbx || cfg.MaxTurns != tc.wantTurns || cfg.StorageDir != wantStore {
				t.Errorf("decoded = {model:%q baseURL:%q apiKey:%q sandbox:%q turns:%d storage:%q}",
					cfg.Model, cfg.BaseURL, cfg.APIKey, cfg.SandboxMode, cfg.MaxTurns, cfg.StorageDir)
			}
			gotDiags := 0
			for _, d := range cfg.Diagnostics() {
				if d.Kind == DiagnosticConflict {
					gotDiags++
				}
			}
			if gotDiags != tc.wantDiags {
				t.Errorf("conflict diagnostics = %d, want %d: %+v", gotDiags, tc.wantDiags, cfg.Diagnostics())
			}
		})
	}
}

// 移除编译期默认值不得掩盖显式类型错误的值：
// 该设置仍然存在，因此 decode 得到零值，诊断
// 报告错误类型，而不是悄悄替换为编译期默认值。
func TestAliasedIllTypedValueDoesNotFallBackToDefault(t *testing.T) {
	t.Run("modelName not a string", func(t *testing.T) {
		home, cwd := t.TempDir(), t.TempDir()
		writeJSONFile(t, filepath.Join(cwd, "argent.json"), map[string]any{"modelName": 42})
		cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home}))
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.Model != "" {
			t.Errorf("model = %q, want empty (no default fallback)", cfg.Model)
		}
		if err := cfg.Validate(); err == nil {
			t.Error("Validate accepted an empty model")
		}
		found := false
		for _, d := range cfg.Diagnostics() {
			if d.Kind == DiagnosticInvalid && strings.TrimPrefix(d.Path, "$.") == "modelName" {
				found = true
			}
		}
		if !found {
			t.Errorf("missing invalid diagnostic for modelName: %+v", cfg.Diagnostics())
		}
	})

	t.Run("max_turns not an integer", func(t *testing.T) {
		home, cwd := t.TempDir(), t.TempDir()
		writeJSONFile(t, filepath.Join(cwd, "argent.json"), map[string]any{"max_turns": "many"})
		cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home}))
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.MaxTurns != 0 {
			t.Errorf("maxTurns = %d, want 0 (no default fallback)", cfg.MaxTurns)
		}
		found := false
		for _, d := range cfg.Diagnostics() {
			if d.Kind == DiagnosticInvalid && strings.TrimPrefix(d.Path, "$.") == "max_turns" {
				found = true
			}
		}
		if !found {
			t.Errorf("missing invalid diagnostic for max_turns: %+v", cfg.Diagnostics())
		}
	})
}

// verify_timeout_ms 是 verifyTimeoutMS 可接受的别名，因此使用它的
// 文件必须胜过编译期默认值。defaults() 仍会为该设置注入
// 规范键，而其他带别名的设置都已把默认值移入 decode；
// mergeTree 保留两种拼写，intField 先读
// 规范默认值，从而悄悄丢弃文件中的值。
func TestVerifyTimeoutAliasBeatsCompiledDefault(t *testing.T) {
	home := t.TempDir()
	cases := []struct {
		name string
		file map[string]any
		want int
	}{
		{"alias from file", map[string]any{"verify_timeout_ms": 5000}, 5000},
		{"canonical from file", map[string]any{"verifyTimeoutMS": 6000}, 6000},
		{"canonical wins over alias", map[string]any{"verifyTimeoutMS": 6000, "verify_timeout_ms": 5000}, 6000},
		{"absent keeps default", nil, 30000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cwd := t.TempDir()
			if tc.file != nil {
				writeJSONFile(t, filepath.Join(cwd, "argent.json"), tc.file)
			}
			cfg, err := Load(cwd, envFunc(map[string]string{"HOME": home}))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.VerifyTimeoutMS != tc.want {
				t.Errorf("VerifyTimeoutMS = %d, want %d", cfg.VerifyTimeoutMS, tc.want)
			}
		})
	}
}

// 别名也必须进入 fail-closed 校验；隐藏在规范默认值之后的负值
// 否则会以虚假的 30000 超时加载成功。
func TestVerifyTimeoutAliasFailClosed(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	writeJSONFile(t, filepath.Join(cwd, "argent.json"), map[string]any{"verify_timeout_ms": -1})
	if _, err := Load(cwd, envFunc(map[string]string{"HOME": home})); err == nil {
		t.Fatal("Load accepted a negative verify_timeout_ms alias")
	}
}

// env 层和 flag 层写入规范键；既然默认的规范键已不存在，
// 它们仍须胜过文件中的别名拼写。
func TestEnvAndOverrideBeatFileAlias(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	writeJSONFile(t, filepath.Join(cwd, "argent.json"), map[string]any{
		"modelName": "file-model",
		"max_turns": 4,
	})

	cfg, err := Load(cwd, envFunc(map[string]string{
		"HOME": home, "ARGENT_MODEL": "env-model", "ARGENT_MAX_TURNS": "7",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Model != "env-model" || cfg.MaxTurns != 7 {
		t.Errorf("env did not win over file aliases: model=%q turns=%d", cfg.Model, cfg.MaxTurns)
	}

	cfg, err = LoadWithOverrides(cwd, envFunc(map[string]string{"HOME": home}), map[string]any{
		"model": "flag-model", "maxTurns": 9,
	})
	if err != nil {
		t.Fatalf("LoadWithOverrides: %v", err)
	}
	if cfg.Model != "flag-model" || cfg.MaxTurns != 9 {
		t.Errorf("flag did not win over file aliases: model=%q turns=%d", cfg.Model, cfg.MaxTurns)
	}
}
