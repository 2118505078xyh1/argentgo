package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"example.com/argentgo/internal/memory"
)

// writeRuleFile 写入一个规则文件（自动创建父目录），供规则发现测试使用。
func writeRuleFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// legacyCompose 复刻接线前的 buildSystemWithTrust 组合公式，用作逐字节基线。
// buildStaticSystem/buildDynamicContext 本身未被本改动触碰，因此该基线是可靠的。
func legacyCompose(root string, env func(string) string, trust bool, thinking, schema string) string {
	static := buildStaticSystem(root, env, trust)
	dynamic := buildDynamicContext(root, thinking, schema)
	if dynamic == "" {
		return static
	}
	return static + "\n" + dynamic + "\n"
}

// TestRuleDiscoveryOffKeepsPromptByteIdentical 证明显式假值词关闭时（0/false/no/off），
// 即使磁盘上存在可被发现的规则，系统提示也与接线前逐字节一致，且不会泄漏规则正文。
func TestRuleDiscoveryOffKeepsPromptByteIdentical(t *testing.T) {
	_, root, home := wiringEnv(t)
	writeRuleFile(t, filepath.Join(root, ".argent", "rules", "extra.md"), "UNIQUE_RULE_BODY_MARKER")
	writeRuleFile(t, filepath.Join(root, ".cursorrules"), "LEGACY_BODY_MARKER")

	baseEnv := envFrom(map[string]string{"USERPROFILE": home, "HOME": home})
	wantWithDynamic := legacyCompose(root, baseEnv, true, "low", "")
	wantNoDynamic := legacyCompose(root, baseEnv, true, "", "")

	for _, value := range []string{"0", "false", "no", "off"} {
		e := envFrom(map[string]string{
			"USERPROFILE":           home,
			"HOME":                  home,
			"ARGENT_RULE_DISCOVERY": value,
		})
		for name, tc := range map[string]struct{ got, want string }{
			"withDynamic": {buildSystemWithTrust(root, e, true, "low", ""), wantWithDynamic},
			"noDynamic":   {buildSystemWithTrust(root, e, true, "", ""), wantNoDynamic},
		} {
			if tc.got != tc.want {
				t.Errorf("%s (ARGENT_RULE_DISCOVERY=%q) drifted from baseline:\ngot:  %q\nwant: %q", name, value, tc.got, tc.want)
			}
			for _, leaked := range []string{"Discovered rule files", "UNIQUE_RULE_BODY_MARKER", "LEGACY_BODY_MARKER"} {
				if strings.Contains(tc.got, leaked) {
					t.Errorf("%s (ARGENT_RULE_DISCOVERY=%q) leaked %q: %q", name, value, leaked, tc.got)
				}
			}
		}
	}
}

// TestRuleDiscoveryEnabledDefaultOn 证明规则发现默认开启：env 为 nil、未设置、空值、垃圾值
// 以及真值词都启用；只有假值词 0/false/no/off（忽略大小写与空白）才关闭。
func TestRuleDiscoveryEnabledDefaultOn(t *testing.T) {
	if !ruleDiscoveryEnabled(nil) {
		t.Fatal("env 为 nil 时必须默认开启")
	}
	if !ruleDiscoveryEnabled(envFrom(nil)) {
		t.Fatal("未设置 ARGENT_RULE_DISCOVERY 时必须默认开启")
	}
	for _, on := range []string{"", "1", " true ", "yes", "ON", "garbage"} {
		if !ruleDiscoveryEnabled(envFrom(map[string]string{"ARGENT_RULE_DISCOVERY": on})) {
			t.Errorf("ARGENT_RULE_DISCOVERY=%q 应开启", on)
		}
	}
	for _, off := range []string{"0", " 0 ", "false", " FALSE ", "no", "off"} {
		if ruleDiscoveryEnabled(envFrom(map[string]string{"ARGENT_RULE_DISCOVERY": off})) {
			t.Errorf("ARGENT_RULE_DISCOVERY=%q 应关闭", off)
		}
	}
}

// TestRuleDiscoveryDefaultOnWiring 证明门控未设置时，规则发现块同样进入系统提示，
// 且只列文件名、不加载规则正文。
func TestRuleDiscoveryDefaultOnWiring(t *testing.T) {
	_, root, home := wiringEnv(t)
	writeRuleFile(t, filepath.Join(root, ".argent", "rules", "default-on.md"), "DEFAULT_ON_BODY_SENTINEL")

	unsetEnv := envFrom(map[string]string{"USERPROFILE": home, "HOME": home})
	got := buildSystemWithTrust(root, unsetEnv, false, "", "")
	for _, want := range []string{
		"Discovered rule files (names only, contents not loaded):",
		"default-on.md (project)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("默认开启的提示缺少 %q: %q", want, got)
		}
	}
	if strings.Contains(got, "DEFAULT_ON_BODY_SENTINEL") {
		t.Errorf("默认开启的提示加载了规则正文: %q", got)
	}
}

// TestRuleDiscoveryOnListsNamesNotContents 证明 opt-in 开启时，发现的规则 basename 进入提示，
// 但规则文件正文不会被加载进提示词。
func TestRuleDiscoveryOnListsNamesNotContents(t *testing.T) {
	_, root, home := wiringEnv(t)
	writeRuleFile(t, filepath.Join(root, ".argent", "rules", "project-rule.md"), "PROJECT_RULE_BODY_SENTINEL")
	writeRuleFile(t, filepath.Join(home, ".argent", "rules", "global-rule.md"), "GLOBAL_RULE_BODY_SENTINEL")

	onEnv := envFrom(map[string]string{
		"USERPROFILE":           home,
		"HOME":                  home,
		"ARGENT_RULE_DISCOVERY": "1",
	})
	got := buildSystemWithTrust(root, onEnv, false, "low", "")
	for _, want := range []string{
		"Discovered rule files (names only, contents not loaded):",
		"project-rule.md (project)",
		"global-rule.md (global)",
		"Reasoning effort: low",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("opt-in prompt missing %q: %q", want, got)
		}
	}
	for _, marker := range []string{"PROJECT_RULE_BODY_SENTINEL", "GLOBAL_RULE_BODY_SENTINEL"} {
		if strings.Contains(got, marker) {
			t.Errorf("opt-in prompt loaded rule content %q: %q", marker, got)
		}
	}
}

// TestRuleDiscoveryLegacyMigrationHint 证明 legacy 规则触发一次性迁移警告。
func TestRuleDiscoveryLegacyMigrationHint(t *testing.T) {
	_, root, home := wiringEnv(t)
	writeRuleFile(t, filepath.Join(root, ".cursorrules"), "legacy body")
	writeRuleFile(t, filepath.Join(root, ".claude"), "unused")

	onEnv := envFrom(map[string]string{
		"USERPROFILE":           home,
		"HOME":                  home,
		"ARGENT_RULE_DISCOVERY": "true",
	})
	got := buildSystemWithTrust(root, onEnv, false, "", "")
	if !strings.Contains(got, ".cursorrules (legacy)") {
		t.Fatalf("legacy rule not listed: %q", got)
	}
	if !strings.Contains(got, memory.LegacyMigrationHint) {
		t.Fatalf("legacy migration hint missing: %q", got)
	}
	if n := strings.Count(got, memory.LegacyMigrationHint); n != 1 {
		t.Fatalf("legacy migration hint should appear once, got %d: %q", n, got)
	}
}

// TestRuleDiscoveryCJKBasename 证明 CJK 规则文件名原样进入提示，不被转义或丢弃。
func TestRuleDiscoveryCJKBasename(t *testing.T) {
	_, root, home := wiringEnv(t)
	const cjkName = "中文规则.md"
	writeRuleFile(t, filepath.Join(root, ".argent", "rules", cjkName), "body")

	onEnv := envFrom(map[string]string{
		"USERPROFILE":           home,
		"HOME":                  home,
		"ARGENT_RULE_DISCOVERY": "on",
	})
	got := buildSystemWithTrust(root, onEnv, false, "", "")
	if !strings.Contains(got, cjkName) {
		t.Fatalf("CJK rule basename missing from prompt: %q", got)
	}
}

// TestRenderRuleDiscoveryBlockDedup 验证渲染层按 basename 去重，且模式专属规则标注 mode。
func TestRenderRuleDiscoveryBlockDedup(t *testing.T) {
	rules := []memory.RuleFile{
		{Path: "project/AGENTS.md", Basename: "AGENTS.md", Scope: memory.RuleScopeProject},
		{Path: "home/AGENTS.md", Basename: "AGENTS.md", Scope: memory.RuleScopeGlobal},
		{Path: "project/rules-architect.md", Basename: "rules-architect.md", Scope: memory.RuleScopeProject, Mode: "architect"},
	}
	got := renderRuleDiscoveryBlock(rules)
	if n := strings.Count(got, "- AGENTS.md ("); n != 1 {
		t.Fatalf("AGENTS.md should be listed once, got %d: %q", n, got)
	}
	if !strings.Contains(got, "rules-architect.md (project/architect)") {
		t.Fatalf("mode-specific rule label missing: %q", got)
	}
}

// TestRuleDiscoveryDedupAcrossScopes 证明 home 与 project 同名规则在提示中只列一次。
func TestRuleDiscoveryDedupAcrossScopes(t *testing.T) {
	_, root, home := wiringEnv(t)
	writeRuleFile(t, filepath.Join(root, "AGENTS.md"), "root body")
	writeRuleFile(t, filepath.Join(home, "AGENTS.md"), "home body")

	onEnv := envFrom(map[string]string{
		"USERPROFILE":           home,
		"HOME":                  home,
		"ARGENT_RULE_DISCOVERY": "1",
	})
	got := buildSystemWithTrust(root, onEnv, false, "", "")
	if n := strings.Count(got, "- AGENTS.md ("); n != 1 {
		t.Fatalf("AGENTS.md should be listed once across scopes, got %d: %q", n, got)
	}
}
