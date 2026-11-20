package skills

import (
	"reflect"
	"testing"
)

func skillNames(skills []Skill) []string {
	names := make([]string, 0, len(skills))
	for _, skill := range skills {
		names = append(names, skill.Name)
	}
	return names
}

func TestMatchPathGlobSyntax(t *testing.T) {
	cases := []struct {
		name     string
		patterns []string
		path     string
		want     bool
	}{
		{"star inside segment", []string{"*.go"}, "main.go", true},
		{"star does not cross slash", []string{"pkg/*.go"}, "pkg/a/main.go", false},
		{"basename at any depth", []string{"main.go"}, "a/b/main.go", true},
		{"basename must be exact", []string{"main.go"}, "a/b/other.go", false},
		{"double star spans directories", []string{"src/**/*.go"}, "src/a/b/main.go", true},
		{"double star spans zero directories", []string{"src/**/*.go"}, "src/main.go", true},
		{"double star keeps prefix anchored", []string{"src/**/*.go"}, "lib/main.go", false},
		{"double star alone matches everything", []string{"**"}, "any/deep/file.txt", true},
		{"leading double star matches root file", []string{"**/test.go"}, "test.go", true},
		{"middle double star spans zero directories", []string{"src/**/test.go"}, "src/test.go", true},
		{"trailing double star matches subtree", []string{"src/**"}, "src/a/b.go", true},
		{"slash anchors pattern", []string{"src/*.go"}, "src/main.go", true},
		{"anchored pattern misses nested file", []string{"src/*.go"}, "src/a/main.go", false},
		{"question mark wildcard", []string{"**/f?.go"}, "a/f1.go", true},
		{"no match", []string{"**/*.go"}, "a/b.txt", false},
		{"empty patterns never match", nil, "a/b.go", false},
		{"bad pattern never matches", []string{"**/[abc"}, "a/[abc", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MatchPath(tc.patterns, tc.path); got != tc.want {
				t.Errorf("MatchPath(%q, %q) = %v, want %v", tc.patterns, tc.path, got, tc.want)
			}
		})
	}
}

func TestMatchPathNormalizesSeparatorsAndCase(t *testing.T) {
	cases := []struct {
		name     string
		patterns []string
		path     string
		want     bool
	}{
		{"windows separators", []string{`src\**\*.go`}, `src\pkg\main.go`, true},
		{"mixed separators", []string{`src\**\*.go`}, "src/pkg/main.go", true},
		{"windows drive letter", []string{`**\*.md`}, `C:\Users\x\README.md`, true},
		{"case insensitive", []string{"**/*.GO"}, "Src/Main.go", true},
		{"leading dot slash", []string{"./src/**"}, "./src/a.go", true},
		{"leading slash", []string{"/src/**"}, "/src/a.go", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MatchPath(tc.patterns, tc.path); got != tc.want {
				t.Errorf("MatchPath(%q, %q) = %v, want %v", tc.patterns, tc.path, got, tc.want)
			}
		})
	}
}

func TestMatchPathCJK(t *testing.T) {
	cases := []struct {
		name     string
		patterns []string
		path     string
		want     bool
	}{
		{"cjk basename", []string{"主.go"}, "项目/文档/主.go", true},
		{"cjk double star", []string{"**/文档/**"}, "项目/文档/子/主.go", true},
		{"cjk star inside segment", []string{"**/文档/*.go"}, "项目/文档/主.go", true},
		{"cjk misses other directory", []string{"**/文档/**"}, "项目/代码/主.go", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MatchPath(tc.patterns, tc.path); got != tc.want {
				t.Errorf("MatchPath(%q, %q) = %v, want %v", tc.patterns, tc.path, got, tc.want)
			}
		})
	}
}

func TestSkillsForPathFiltersAndKeepsOrder(t *testing.T) {
	skills := []Skill{
		{Name: "always"},
		{Name: "go", Triggers: Triggers{Paths: []string{"**/*.go"}}},
		{Name: "md", Triggers: Triggers{Paths: []string{"**/*.md"}}},
	}
	cases := []struct {
		path string
		want []string
	}{
		{"src/main.go", []string{"always", "go"}},
		{"README.md", []string{"always", "md"}},
		{"notes.txt", []string{"always"}},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			if got := skillNames(SkillsForPath(skills, tc.path)); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("SkillsForPath(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

func TestSkillsForPathEmptyTriggersMatchEveryPath(t *testing.T) {
	skills := []Skill{
		{Name: "plain"},
		{Name: "keywords-only", Triggers: Triggers{Keywords: []string{"go"}}},
		{Name: "models-only", Triggers: Triggers{Models: []string{"claude"}}},
	}
	for _, path := range []string{"", "src/main.go", `C:\tmp\notes.txt`} {
		if got := skillNames(SkillsForPath(skills, path)); !reflect.DeepEqual(got, []string{"plain", "keywords-only", "models-only"}) {
			t.Errorf("SkillsForPath(%q) = %v, want every skill", path, got)
		}
	}
}

func TestMatchKeywords(t *testing.T) {
	cases := []struct {
		name     string
		keywords []string
		text     string
		want     bool
	}{
		{"ascii hit", []string{"refactor"}, "please refactor this", true},
		{"case insensitive", []string{"REFACTOR"}, "please refactor this", true},
		{"cjk hit", []string{"重构"}, "请重构这里", true},
		{"one of many", []string{"deploy", "重构"}, "请重构这里", true},
		{"miss", []string{"deploy"}, "please refactor", false},
		{"empty keywords", nil, "anything", false},
		{"blank keyword", []string{"   "}, "anything", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MatchKeywords(tc.keywords, tc.text); got != tc.want {
				t.Errorf("MatchKeywords(%q, %q) = %v, want %v", tc.keywords, tc.text, got, tc.want)
			}
		})
	}
}

func TestSkillsForKeywordsFiltersAndKeepsOrder(t *testing.T) {
	skills := []Skill{
		{Name: "always"},
		{Name: "go", Triggers: Triggers{Keywords: []string{"go", "重构"}}},
		{Name: "ship", Triggers: Triggers{Keywords: []string{"deploy"}}},
	}
	cases := []struct {
		text string
		want []string
	}{
		{"请重构 go 模块", []string{"always", "go"}},
		{"deploy now", []string{"always", "ship"}},
		{"unrelated", []string{"always"}},
	}
	for _, tc := range cases {
		t.Run(tc.text, func(t *testing.T) {
			if got := skillNames(SkillsForKeywords(skills, tc.text)); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("SkillsForKeywords(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

func TestSkillsForModelFiltersByFamily(t *testing.T) {
	skills := []Skill{
		{Name: "always"},
		{Name: "claude-only", Triggers: Triggers{Models: []string{"claude"}}},
		{Name: "both", Triggers: Triggers{Models: []string{"claude", "gemini"}}},
		{Name: "gemini-only", Triggers: Triggers{Models: []string{"gemini"}}},
		{Name: "any", Triggers: Triggers{Models: []string{"*"}}},
	}
	cases := []struct {
		family string
		want   []string
	}{
		{"claude", []string{"always", "claude-only", "both", "any"}},
		{"CLAUDE", []string{"always", "claude-only", "both", "any"}},
		{"gemini", []string{"always", "both", "gemini-only", "any"}},
		{"gpt", []string{"always", "any"}},
		{"", []string{"always", "any"}},
	}
	for _, tc := range cases {
		t.Run(tc.family, func(t *testing.T) {
			if got := skillNames(SkillsForModel(skills, tc.family)); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("SkillsForModel(%q) = %v, want %v", tc.family, got, tc.want)
			}
		})
	}
}

func TestParseTriggersFromFrontmatter(t *testing.T) {
	text := `---
name: routed
paths:
  - "**/*.go"
  - src/**
keywords: [refactor, 重构]
models: [claude]
---
body`
	skill, err := Parse(text, "routed/SKILL.md")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if want := []string{"**/*.go", "src/**"}; !reflect.DeepEqual(skill.Triggers.Paths, want) {
		t.Errorf("paths = %q, want %q", skill.Triggers.Paths, want)
	}
	if want := []string{"refactor", "重构"}; !reflect.DeepEqual(skill.Triggers.Keywords, want) {
		t.Errorf("keywords = %q, want %q", skill.Triggers.Keywords, want)
	}
	if want := []string{"claude"}; !reflect.DeepEqual(skill.Triggers.Models, want) {
		t.Errorf("models = %q, want %q", skill.Triggers.Models, want)
	}
	if skill.Name != "routed" || skill.Body != "body" {
		t.Errorf("parse changed core fields: name=%q body=%q", skill.Name, skill.Body)
	}
}

func TestParseLeavesTriggersEmptyWhenAbsent(t *testing.T) {
	skill, err := Parse("---\nname: plain\n---\nbody", "plain/SKILL.md")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(skill.Triggers.Paths) != 0 || len(skill.Triggers.Keywords) != 0 || len(skill.Triggers.Models) != 0 {
		t.Errorf("unexpected triggers: %#v", skill.Triggers)
	}
	if got := skillNames(SkillsForPath([]Skill{skill}, "anything")); !reflect.DeepEqual(got, []string{"plain"}) {
		t.Errorf("skill with no triggers should match every path, got %v", got)
	}
}

func TestResolveTriggeredSkillsIntersectsFiltersInRegistryOrder(t *testing.T) {
	// 插入顺序刻意不排序：聚合必须保留它。
	registry := New()
	registry.Add(Skill{Name: "always"})
	registry.Add(Skill{Name: "go", Triggers: Triggers{Paths: []string{"**/*.go"}}})
	registry.Add(Skill{Name: "refactor", Triggers: Triggers{Keywords: []string{"重构", "refactor"}}})
	registry.Add(Skill{Name: "claude", Triggers: Triggers{Models: []string{"claude"}}})
	registry.Add(Skill{Name: "go-refactor", Triggers: Triggers{Paths: []string{"**/*.go"}, Keywords: []string{"重构"}}})
	registry.Add(Skill{Name: "go-claude", Triggers: Triggers{Paths: []string{"**/*.go"}, Models: []string{"claude"}}})
	registry.Add(Skill{Name: "all-go", Triggers: Triggers{Paths: []string{"**/*.go"}, Keywords: []string{"重构"}, Models: []string{"claude"}}})
	registry.Add(Skill{Name: "wildcard", Triggers: Triggers{Models: []string{"*"}}})

	cases := []struct {
		name        string
		path        string
		keywords    string
		modelFamily string
		want        []string
	}{
		{
			name:        "gemini misses every claude-only skill but keeps CJK keyword",
			path:        "src/main.go",
			keywords:    "请重构代码",
			modelFamily: "gemini",
			want:        []string{"always", "go", "refactor", "go-refactor", "wildcard"},
		},
		{
			name:        "claude adds the model-restricted skills, all-go needs all three",
			path:        "src/main.go",
			keywords:    "请重构代码",
			modelFamily: "CLAUDE",
			want:        []string{"always", "go", "refactor", "claude", "go-refactor", "go-claude", "all-go", "wildcard"},
		},
		{
			name:        "wrong path drops path-restricted skills",
			path:        "README.md",
			keywords:    "请重构代码",
			modelFamily: "claude",
			want:        []string{"always", "refactor", "claude", "wildcard"},
		},
		{
			name:        "no context keeps only unrestricted and wildcard-model skills",
			path:        "",
			keywords:    "",
			modelFamily: "",
			want:        []string{"always", "wildcard"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := skillNames(ResolveTriggeredSkills(registry, tc.path, tc.keywords, tc.modelFamily))
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ResolveTriggeredSkills(%q, %q, %q) = %v, want %v",
					tc.path, tc.keywords, tc.modelFamily, got, tc.want)
			}
		})
	}
}

func TestResolveTriggeredSkillsDeduplicatesByNameAndKeepsFirst(t *testing.T) {
	skills := []Skill{
		{Name: "重构", Triggers: Triggers{Keywords: []string{"重构"}}},
		{Name: "always"},
		{Name: "重构", Triggers: Triggers{Keywords: []string{"重构"}}},
		{Name: "其他", Triggers: Triggers{Keywords: []string{"重构"}}},
	}
	// CJK keyword hits every keyword-restricted skill; the two "重构" entries
	// collapse to their first position, ahead of "其他".
	got := skillNames(resolveTriggeredSkills(skills, "", "请重构这里", ""))
	want := []string{"重构", "always", "其他"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("resolveTriggeredSkills = %v, want %v", got, want)
	}
}

func TestResolveTriggeredSkillsNilRegistry(t *testing.T) {
	if got := ResolveTriggeredSkills(nil, "src/main.go", "重构", "claude"); got != nil {
		t.Errorf("nil registry = %v, want nil", got)
	}
}
