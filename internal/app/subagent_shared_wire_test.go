package app

import "testing"

func TestSubagentSharedPromptEnabledFromEnv(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"", true}, {"1", true}, {"true", true}, {"YES", true},
		{"  on  ", true}, {"maybe", true}, {"0", false}, {"false", false}, {" NO ", false}, {"off", false},
	}
	for _, tc := range cases {
		env := func(string) string { return tc.raw }
		if got := subagentSharedPromptEnabledFromEnv(env); got != tc.want {
			t.Errorf("enabled(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
	if !subagentSharedPromptEnabledFromEnv(nil) {
		t.Fatal("nil env must default to on")
	}
}

// TestConfigureSubagentSharedPromptWiresSwitch 验证接线：env 真值能设置 agent 侧开关，
// 每次调用由本次 env 唯一决定（不受上次残留影响）。
func TestConfigureSubagentSharedPromptWiresSwitch(t *testing.T) {
	// agent 侧没有导出只读查询，这里只验证调用不 panic 且可重复切换；
	// 真正的 system 前缀行为由 agent 包内的测试覆盖。
	t.Cleanup(func() { configureSubagentSharedPrompt(nil) })
	env := func(v string) func(string) string { return func(string) string { return v } }
	configureSubagentSharedPrompt(env("1"))
	configureSubagentSharedPrompt(env(""))
	configureSubagentSharedPrompt(nil)
}
