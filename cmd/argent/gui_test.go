package main

import (
	"strings"
	"testing"
)

// 只有前导的 `gui` 子命令能把 CLI 转入原生
// 前端。扫描每个参数会劫持提示和标志值。
func TestIsGUIRequestOnlyMatchesLeadingGUISubcommand(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"no args", nil, false},
		{"gui subcommand", []string{"gui"}, true},
		{"gui subcommand with flags", []string{"gui", "--model", "m1"}, true},
		{"prompt containing gui", []string{"fix", "gui", "bug"}, false},
		{"flag value named gui", []string{"--model", "gui", "hello"}, false},
		{"trailing gui prompt word", []string{"hello", "gui"}, false},
		{"gui flag", []string{"--gui"}, false},
		{"gui flag with prompt", []string{"--gui", "hello"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isGUIRequest(tc.args); got != tc.want {
				t.Fatalf("isGUIRequest(%q) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}

// envValue 返回环境变量最后一次出现时的值；后追加的项会覆盖 base 中的同名项。
func envValue(env []string, key string) (string, bool) {
	prefix := key + "="
	var value string
	found := false
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			value = strings.TrimPrefix(item, prefix)
			found = true
		}
	}
	return value, found
}

// 原生前端的 cmd.Env 必须透传已解析的 locale，否则 --locale 会静默失效。
func TestNativeFrontendEnvIncludesLocale(t *testing.T) {
	env := nativeFrontendEnv([]string{"PATH=/usr/bin"}, "C:\\bin\\argentgo.exe", "D:\\work", "zh-CN")

	for _, want := range []struct{ key, value string }{
		{"ARGENT_EXE", "C:\\bin\\argentgo.exe"},
		{"ARGENT_CWD", "D:\\work"},
		{"ARGENT_LOCALE", "zh-CN"},
	} {
		got, ok := envValue(env, want.key)
		if !ok {
			t.Fatalf("missing %s in env %q", want.key, env)
		}
		if got != want.value {
			t.Errorf("%s = %q, want %q", want.key, got, want.value)
		}
	}
}

// base 为 nil 时仍须带齐所有 ARGENT_* 变量，不能因空切片丢弃 locale。
func TestNativeFrontendEnvWithNilBase(t *testing.T) {
	env := nativeFrontendEnv(nil, "/opt/argentgo", "/tmp/work", "en-US")
	if len(env) != 3 {
		t.Fatalf("len(env) = %d, want 3: %q", len(env), env)
	}
	for _, want := range []struct{ key, value string }{
		{"ARGENT_EXE", "/opt/argentgo"},
		{"ARGENT_CWD", "/tmp/work"},
		{"ARGENT_LOCALE", "en-US"},
	} {
		got, ok := envValue(env, want.key)
		if !ok || got != want.value {
			t.Errorf("%s = (%q, %v), want %q", want.key, got, ok, want.value)
		}
	}
}

// locale 为空也要显式写出 ARGENT_LOCALE=，让原生侧能区分"未传"与"空值"。
func TestNativeFrontendEnvEmptyLocaleStillPresent(t *testing.T) {
	env := nativeFrontendEnv([]string{}, "exe", "root", "")
	got, ok := envValue(env, "ARGENT_LOCALE")
	if !ok {
		t.Fatalf("missing ARGENT_LOCALE in env %q", env)
	}
	if got != "" {
		t.Errorf("ARGENT_LOCALE = %q, want empty", got)
	}
}

// base 中若已有同名变量，追加的值必须覆盖它，确保 Resolve 的结果生效。
func TestNativeFrontendEnvOverridesExistingLocale(t *testing.T) {
	env := nativeFrontendEnv([]string{"ARGENT_LOCALE=stale"}, "exe", "root", "ja-JP")
	got, ok := envValue(env, "ARGENT_LOCALE")
	if !ok || got != "ja-JP" {
		t.Errorf("ARGENT_LOCALE = (%q, %v), want ja-JP", got, ok)
	}
}
