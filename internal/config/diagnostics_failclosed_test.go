package config

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// failClosedValidConfig 是手工构造的 Config，满足 fail-closed 摘要
// 检查的每一项不变量。它有意把所有其他字段
// 保持为零值：这里 Diagnostics 只检查被测的设置。
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
			// 手工构造的 Config 的 PrivacyMode 为空（Load 总是
			// 把它规范化为 "any"）。摘要仍必须 fail closed。
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

// 摘要不得在第一个 fail-closed 问题处停止：所有问题按
// 稳定顺序一起报告，以便 doctor 一次性打印。
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
	// 旧检查仍然先运行，并且仍拥有 Validate 的首个错误。
	cfg.SandboxMode = "loose"
	if first := cfg.Diagnostics()[0]; first.Path != "$.sandboxMode" {
		t.Errorf("first diagnostic = %+v, want $.sandboxMode", first)
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "sandboxMode") {
		t.Errorf("Validate error = %v, want the legacy sandboxMode complaint", err)
	}
}

// fail-closed 摘要在加载时强制执行，而不仅在调用方
// 显式请求诊断时执行。这就是"有效"之处：不安全的
// 配置无法经由正常启动路径到达 agent 循环。
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

	// 只使用旧值级失败的配置仍能加载；
	// 那些仍属于显式的 Validate() 约定，而非加载时约定。
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
