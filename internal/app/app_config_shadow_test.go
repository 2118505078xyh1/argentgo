package app

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestIsKeyShadowedEnv 证明 env 层会遮蔽配置文件中的同名键，
// 且返回遮蔽来源原文。"env:ARGENT_MODEL" 这类带说明的标签同样识别。
func TestIsKeyShadowedEnv(t *testing.T) {
	cases := []struct {
		name    string
		sources []string
	}{
		{"bare env", []string{"defaults", "config.json", "env"}},
		{"keyed env", []string{"config.json", "env:ARGENT_MODEL"}},
		{"chinese env", []string{"config.json", "环境变量(ARGENT_MODEL)"}},
		{"described env", []string{"config.json", "via env"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			shadowed, source := isKeyShadowed("model", tc.sources)
			if !shadowed {
				t.Fatalf("env source must shadow key, sources=%v", tc.sources)
			}
			want := tc.sources[len(tc.sources)-1]
			if source != want {
				t.Fatalf("shadow source = %q, want %q", source, want)
			}
		})
	}
}

// TestIsKeyShadowedFlag 证明 flag 层会遮蔽配置文件中的同名键。
func TestIsKeyShadowedFlag(t *testing.T) {
	cases := []struct {
		name    string
		sources []string
	}{
		{"bare flag", []string{"config.json", "flag"}},
		{"keyed flag", []string{"config.json", "flag:--model"}},
		{"chinese flag", []string{"config.json", "命令行标志"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			shadowed, source := isKeyShadowed("model", tc.sources)
			if !shadowed {
				t.Fatalf("flag source must shadow key, sources=%v", tc.sources)
			}
			want := tc.sources[len(tc.sources)-1]
			if source != want {
				t.Fatalf("shadow source = %q, want %q", source, want)
			}
		})
	}
}

// TestIsKeyShadowedNoShadow 证明只有配置文件/默认值层时不会被遮蔽，
// 且路径中偶然出现的 "env" 子串不会被误判为 env 来源。
func TestIsKeyShadowedNoShadow(t *testing.T) {
	cases := []struct {
		name    string
		sources []string
	}{
		{"files only", []string{"defaults", "config.json", "argent.json"}},
		{"path containing env", []string{`C:\home\env\config.json`, "defaults"}},
		{"slash path containing env", []string{"/home/env/config.json"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			shadowed, source := isKeyShadowed("model", tc.sources)
			if shadowed {
				t.Fatalf("sources must not shadow key: %v (got %q)", tc.sources, source)
			}
			if source != "" {
				t.Fatalf("no shadow must return empty source, got %q", source)
			}
		})
	}
}

// TestIsKeyShadowedCJKKey 证明 CJK 键不会按字节被破坏，仍能正确识别遮蔽；
// 同时验证带说明的 env 来源对 CJK 键同样生效。
func TestIsKeyShadowedCJKKey(t *testing.T) {
	const key = "模型.名称"
	sources := []string{"配置文件.json", "env:ARGENT_MODEL"}
	shadowed, source := isKeyShadowed(key, sources)
	if !shadowed {
		t.Fatalf("CJK key must be recognized as shadowed")
	}
	if source != "env:ARGENT_MODEL" {
		t.Fatalf("shadow source = %q, want %q", source, "env:ARGENT_MODEL")
	}
	if !utf8.ValidString(key) {
		t.Fatal("CJK key must remain valid UTF-8")
	}
}

// TestIsKeyShadowedEmptySources 证明空来源列表（nil 与空切片）返回未遮蔽，
// 且空键同样返回未遮蔽。
func TestIsKeyShadowedEmptySources(t *testing.T) {
	for _, tc := range []struct {
		name    string
		key     string
		sources []string
	}{
		{"nil sources", "model", nil},
		{"empty sources", "model", []string{}},
		{"empty key", "", []string{"env"}},
		{"whitespace key", "  ", []string{"env"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shadowed, source := isKeyShadowed(tc.key, tc.sources)
			if shadowed || source != "" {
				t.Fatalf("got (%v, %q), want (false, \"\")", shadowed, source)
			}
		})
	}
}

// TestIsKeyShadowedStableHighestPriority 证明多来源时稳定选取最高优先级遮蔽项：
// flag 高于 env，同级取靠后的一项；重复调用结果一致。
func TestIsKeyShadowedStableHighestPriority(t *testing.T) {
	cases := []struct {
		name    string
		sources []string
		want    string
	}{
		{"flag over env", []string{"flag:--a", "config.json", "env"}, "flag:--a"},
		{"flag over env reversed", []string{"env", "config.json", "flag:--b"}, "flag:--b"},
		{"last env wins", []string{"env:first", "env:second"}, "env:second"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for i := 0; i < 3; i++ {
				shadowed, source := isKeyShadowed("model", tc.sources)
				if !shadowed || source != tc.want {
					t.Fatalf("got (%v, %q), want (true, %q)", shadowed, source, tc.want)
				}
			}
		})
	}
}

// TestConfigShadowWarningOptIn 证明遮蔽警告默认开启、=0 关闭，
// 只有 ARGENT_CONFIG_SHADOW_CHECK=0 时才静默，且 CJK 键原样出现在警告中。
func TestConfigShadowWarningOptIn(t *testing.T) {
	key := "模型"
	sources := []string{"config.json", "env:ARGENT_MODEL"}
	on := func(string) string { return "" }
	off := func(string) string { return "0" }

	if got := configShadowWarning(key, sources, nil); got == "" {
		t.Fatal("nil env must keep warning on (default), got empty")
	}
	if got := configShadowWarning(key, sources, on); got == "" {
		t.Fatal("default (on) must keep warning on, got empty")
	}
	if got := configShadowWarning(key, sources, off); got != "" {
		t.Fatalf("=0 must keep warning off, got %q", got)
	}
	got := configShadowWarning(key, sources, on)
	if got == "" {
		t.Fatal("enabled warning must not be empty")
	}
	if !strings.Contains(got, key) {
		t.Fatalf("warning must contain intact CJK key %q, got %q", key, got)
	}
	if !strings.Contains(got, "env:ARGENT_MODEL") {
		t.Fatalf("warning must name the shadow source, got %q", got)
	}

	// 启用检查但未遮蔽时不产生警告。
	if got := configShadowWarning("model", []string{"config.json"}, on); got != "" {
		t.Fatalf("no shadow must produce no warning, got %q", got)
	}
}
