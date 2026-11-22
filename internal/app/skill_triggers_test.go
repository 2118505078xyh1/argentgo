package app

import (
	"testing"
)

// 本文件守护技能触发器接线的纯函数面：opt-in 默认关闭、env 与配置两条入口、
// 模型家族归一化与提示路径抽取。所有用例不读进程环境、不联网、不用真实模型。

// fixedEnv 返回一个只认识指定键的 env 读取器，避免测试依赖进程环境。
func fixedEnv(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestSkillTriggersEnabledDefaultOff(t *testing.T) {
	if skillTriggersEnabled(nil, fixedEnv(nil)) {
		t.Fatal("default should be off")
	}
	if skillTriggersEnabled(map[string]any{"skillTriggers": false}, fixedEnv(nil)) {
		t.Fatal("explicit false should stay off")
	}
	if skillTriggersEnabled(nil, fixedEnv(map[string]string{skillTriggersOptInEnv: "0"})) {
		t.Fatal("env 0 should stay off")
	}
}

func TestSkillTriggersEnabledTwoEntries(t *testing.T) {
	if !skillTriggersEnabled(nil, fixedEnv(map[string]string{skillTriggersOptInEnv: "1"})) {
		t.Fatal("env 1 should enable")
	}
	if !skillTriggersEnabled(map[string]any{"skill_triggers": true}, fixedEnv(nil)) {
		t.Fatal("config true should enable")
	}
	if !skillTriggersEnabled(map[string]any{"skill-triggers": "yes"}, fixedEnv(nil)) {
		t.Fatal("config string true should enable")
	}
}

func TestSkillTriggerModelFamily(t *testing.T) {
	cases := map[string]string{
		"anthropic/claude-sonnet-4.5": "claude",
		"gpt-5.6":                     "gpt",
		"Gemini-2.5-Flash":            "gemini",
		"qwen3.8-max":                 "qwen",
		"my-custom-model":             "my-custom-model",
		"":                            "",
	}
	for model, want := range cases {
		if got := skillTriggerModelFamily(model); got != want {
			t.Errorf("skillTriggerModelFamily(%q) = %q, want %q", model, got, want)
		}
	}
}

func TestSkillTriggerPath(t *testing.T) {
	cases := []struct {
		prompt string
		root   string
		want   string
	}{
		{"编辑 src/main.go，然后提交", "/workspace", "src/main.go"},
		{"fix `docs/guide.md` please", "/workspace", "docs/guide.md"},
		{"请重构这个模块", "/workspace", "/workspace"},
		{"", "/workspace", "/workspace"},
	}
	for _, tc := range cases {
		if got := skillTriggerPath(tc.prompt, tc.root); got != tc.want {
			t.Errorf("skillTriggerPath(%q, %q) = %q, want %q", tc.prompt, tc.root, got, tc.want)
		}
	}
}
