package memory

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// writeRule 在 dir 下写入一个规则文件，测试辅助。
func writeRule(t *testing.T, dir, rel, content string) string {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
	return path
}

// 三层（project / global / legacy）都能被发现，并各自带上正确 Scope。
func TestDiscoverRulesThreeScopes(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()

	writeRule(t, home, "AGENTS.md", "global agents")
	writeRule(t, project, "CLAUDE.md", "project claude")
	writeRule(t, project, ".cursorrules", "legacy cursor")

	got := DiscoverRules(home, project, "")
	if len(got) != 3 {
		t.Fatalf("got %d rules, want 3: %#v", len(got), got)
	}
	byBase := make(map[string]RuleFile, len(got))
	for _, r := range got {
		byBase[r.Basename] = r
	}
	if r := byBase["AGENTS.md"]; r.Scope != RuleScopeGlobal || r.Mode != "" {
		t.Errorf("AGENTS.md = %#v, want global/common", r)
	}
	if r := byBase["CLAUDE.md"]; r.Scope != RuleScopeProject || r.Mode != "" {
		t.Errorf("CLAUDE.md = %#v, want project/common", r)
	}
	if r := byBase[".cursorrules"]; r.Scope != RuleScopeLegacy {
		t.Errorf(".cursorrules = %#v, want legacy", r)
	}
	if RuleMigrationHint(byBase[".cursorrules"]) == "" {
		t.Error("legacy rule should carry a migration hint")
	}
}

// 按 basename 去重：project 版覆盖 global 版（nearer wins），
// global 版又覆盖 legacy 版中的同名规则文件。
func TestDiscoverRulesBasenameDedupPriority(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()

	writeRule(t, home, "AGENTS.md", "global agents")
	writeRule(t, project, "AGENTS.md", "project agents")
	writeRule(t, home, "CLAUDE.md", "global claude")
	writeRule(t, project, "CLAUDE.md", "project claude")
	// global 的 .argent/rules/rules.md 与 legacy 的 .cursor/rules/rules.md 同名。
	writeRule(t, home, filepath.Join(".argent", "rules", "rules.md"), "global rules")
	writeRule(t, project, filepath.Join(".cursor", "rules", "rules.md"), "legacy rules")

	got := DiscoverRules(home, project, "")
	byBase := make(map[string]RuleFile, len(got))
	for _, r := range got {
		byBase[r.Basename] = r
	}
	if len(byBase) != len(got) {
		t.Fatalf("duplicate basenames in result: %#v", got)
	}
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		r := byBase[name]
		if r.Scope != RuleScopeProject || !strings.HasPrefix(r.Path, project) {
			t.Errorf("%s = %#v, want project copy under %q", name, r, project)
		}
		if strings.Contains(r.Path, home) {
			t.Errorf("%s unexpectedly resolved to global path %q", name, r.Path)
		}
	}
	if r := byBase["rules.md"]; r.Scope != RuleScopeGlobal || !strings.Contains(r.Path, filepath.Join(".argent", "rules")) {
		t.Errorf("rules.md = %#v, want global copy (.argent/rules) over legacy", r)
	}
}

// mode 过滤：空 mode 完全跳过模式专属目录；非空 mode 额外发现该模式的
// 规则目录，且不混入其他模式的规则。
func TestDiscoverRulesModeFiltering(t *testing.T) {
	project := t.TempDir()
	writeRule(t, project, filepath.Join(".kilocode", "rules", "general.md"), "general")
	writeRule(t, project, filepath.Join(".kilocode", "rules-architect", "arch.md"), "architect")
	writeRule(t, project, filepath.Join(".kilocode", "rules-ask", "ask.md"), "ask")

	common := DiscoverRules("", project, "")
	if len(common) != 1 || common[0].Basename != "general.md" || common[0].Mode != "" {
		t.Fatalf("mode=\"\" = %#v, want only general.md", common)
	}

	architect := DiscoverRules("", project, "architect")
	byBase := make(map[string]RuleFile, len(architect))
	for _, r := range architect {
		byBase[r.Basename] = r
	}
	if len(byBase) != 2 {
		t.Fatalf("mode=architect got %d rules, want 2: %#v", len(byBase), architect)
	}
	if r := byBase["general.md"]; r.Mode != "" || r.Scope != RuleScopeProject {
		t.Errorf("general.md = %#v, want common project rule", r)
	}
	if r := byBase["arch.md"]; r.Mode != "architect" || r.Scope != RuleScopeProject {
		t.Errorf("arch.md = %#v, want project rule with mode architect", r)
	}
	if _, ok := byBase["ask.md"]; ok {
		t.Error("rules-ask should be excluded when mode=architect")
	}
}

// legacy 迁移提示：常量非空，仅 legacy 作用域返回提示，其余为空。
func TestDiscoverRulesLegacyMigrationHint(t *testing.T) {
	if strings.TrimSpace(LegacyMigrationHint) == "" {
		t.Fatal("LegacyMigrationHint must not be empty")
	}
	legacy := RuleFile{Scope: RuleScopeLegacy, Basename: ".cursorrules"}
	if got := RuleMigrationHint(legacy); got != LegacyMigrationHint {
		t.Errorf("legacy hint = %q, want %q", got, LegacyMigrationHint)
	}
	for _, scope := range []RuleScope{RuleScopeGlobal, RuleScopeProject} {
		if got := RuleMigrationHint(RuleFile{Scope: scope}); got != "" {
			t.Errorf("%s hint = %q, want empty", scope, got)
		}
	}
}

// CJK 路径与 CJK 规则文件名均能被发现，并参与 basename 去重。
func TestDiscoverRulesCJKPaths(t *testing.T) {
	home := filepath.Join(t.TempDir(), "用户目录")
	project := filepath.Join(t.TempDir(), "项目")

	writeRule(t, home, "AGENTS.md", "全局")
	writeRule(t, project, "AGENTS.md", "项目")
	writeRule(t, project, filepath.Join(".argent", "rules", "约定.md"), "约定")
	writeRule(t, project, ".cursorrules", "旧规则")

	got := DiscoverRules(home, project, "")
	byBase := make(map[string]RuleFile, len(got))
	for _, r := range got {
		byBase[r.Basename] = r
	}
	if len(byBase) != 3 {
		t.Fatalf("got %d rules, want 3: %#v", len(byBase), got)
	}
	if r := byBase["AGENTS.md"]; r.Scope != RuleScopeProject || !strings.HasPrefix(r.Path, project) {
		t.Errorf("CJK AGENTS.md = %#v, want project copy", r)
	}
	if r := byBase["约定.md"]; r.Scope != RuleScopeProject || !strings.Contains(r.Path, filepath.Join(".argent", "rules", "约定.md")) {
		t.Errorf("CJK rule = %#v", r)
	}
}

// 确定性与不改入参：相同入参重复调用结果一致；文件创建顺序不影响
// 稳定排序；入参字符串保持不变（纯函数，不写磁盘）。
func TestDiscoverRulesDeterministicAndNoInputMutation(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()

	// 以打乱的顺序创建，验证输出仍按稳定规则排序。
	writeRule(t, project, filepath.Join(".argent", "rules", "zeta.md"), "z")
	writeRule(t, home, "AGENTS.md", "g")
	writeRule(t, project, filepath.Join(".argent", "rules", "alpha.md"), "a")
	writeRule(t, project, "AGENTS.md", "p")

	homeIn, projectIn, modeIn := home, project, ""
	first := DiscoverRules(homeIn, projectIn, modeIn)
	second := DiscoverRules(homeIn, projectIn, modeIn)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("non-deterministic result:\nfirst  = %#v\nsecond = %#v", first, second)
	}
	if homeIn != home || projectIn != project || modeIn != "" {
		t.Fatalf("inputs mutated: home=%q project=%q mode=%q", homeIn, projectIn, modeIn)
	}

	// 断言稳定顺序：project 先于 global；同作用域内按 basename 升序。
	var sawGlobal bool
	for i, r := range first {
		if r.Scope == RuleScopeGlobal {
			sawGlobal = true
			continue
		}
		if sawGlobal {
			t.Errorf("rule %d (%s, %s) appears after a global rule", i, r.Scope, r.Basename)
		}
	}
	// alpha.md / AGENTS.md / zeta.md 均为 project，按 basename 升序。
	var projectBases []string
	for _, r := range first {
		if r.Scope == RuleScopeProject {
			projectBases = append(projectBases, r.Basename)
		}
	}
	want := []string{"AGENTS.md", "alpha.md", "zeta.md"}
	if !reflect.DeepEqual(projectBases, want) {
		t.Errorf("project basename order = %v, want %v", projectBases, want)
	}
}

// 空入参返回 nil，不 panic。
func TestDiscoverRulesEmptyInputs(t *testing.T) {
	if got := DiscoverRules("", "", ""); got != nil {
		t.Errorf("empty inputs = %#v, want nil", got)
	}
}

// 逃逸符号链接不得被跟随：base 规则目录内指向外部文件的链接应被跳过。
func TestDiscoverRulesSymlinkEscape(t *testing.T) {
	project := t.TempDir()
	outside := t.TempDir()
	external := writeRule(t, outside, "secret.md", "outside")
	writeRule(t, project, "kept.md", "inside")

	link := filepath.Join(project, "escaped.md")
	if err := os.Symlink(external, link); err != nil {
		t.Skipf("symlink creation not permitted: %v", err)
	}

	got := DiscoverRules("", project, "")
	for _, r := range got {
		if strings.EqualFold(r.Basename, filepath.Base(external)) {
			t.Fatalf("escaping symlink was followed: %#v", r)
		}
	}
	if len(got) != 1 || got[0].Basename != "kept.md" {
		t.Fatalf("got %#v, want only kept.md", got)
	}
}
