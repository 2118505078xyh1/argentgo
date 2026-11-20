package skills

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestSkillArgumentHints(t *testing.T) {
	cases := []struct {
		name   string
		skills []Skill
		want   map[string][]string
	}{
		{
			name:   "nil input",
			skills: nil,
			want:   map[string][]string{},
		},
		{
			name:   "empty slice",
			skills: []Skill{},
			want:   map[string][]string{},
		},
		{
			name:   "skill without hint",
			skills: []Skill{{Name: "plain", Description: "no arguments"}},
			want:   map[string][]string{},
		},
		{
			name:   "open placeholders only",
			skills: []Skill{{Name: "review", ArgumentHint: "<path> [focus]"}},
			want:   map[string][]string{},
		},
		{
			name:   "wrapped enumerated group",
			skills: []Skill{{Name: "deploy", ArgumentHint: "[dev|stage|prod]"}},
			want:   map[string][]string{"deploy": {"dev", "stage", "prod"}},
		},
		{
			name:   "single value group is not an enumeration",
			skills: []Skill{{Name: "one", ArgumentHint: "[only|only]"}},
			want:   map[string][]string{},
		},
		{
			name:   "cjk candidates",
			skills: []Skill{{Name: "环境", ArgumentHint: "环境: 开发|测试|生产"}},
			want:   map[string][]string{"环境": {"开发", "测试", "生产"}},
		},
		{
			name:   "full width hint folds to ascii delimiters",
			skills: []Skill{{Name: "fw", ArgumentHint: "［DEV｜Stage］"}},
			want:   map[string][]string{"fw": {"DEV", "Stage"}},
		},
		{
			name: "multiple skills only enumerated ones appear",
			skills: []Skill{
				{Name: "deploy", ArgumentHint: "dev|stage|prod"},
				{Name: "review", ArgumentHint: "<file>"},
				{Name: "build", ArgumentHint: "target: debug|release [fast|clean]"},
			},
			want: map[string][]string{
				"deploy": {"dev", "stage", "prod"},
				"build":  {"debug", "release", "fast", "clean"},
			},
		},
		{
			name:   "duplicates and empty alternatives dropped",
			skills: []Skill{{Name: "dedupe", ArgumentHint: "[a|b|a||c]"}},
			want:   map[string][]string{"dedupe": {"a", "b", "c"}},
		},
		{
			name: "cjk and ascii skills together",
			skills: []Skill{
				{Name: "发布", ArgumentHint: "[灰度|全量]"},
				{Name: "lint", ArgumentHint: "fix|check"},
				{Name: "empty"},
			},
			want: map[string][]string{
				"发布":   {"灰度", "全量"},
				"lint": {"fix", "check"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SkillArgumentHints(tc.skills)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("SkillArgumentHints() = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestSkillArgumentHintsDoesNotMutateInput(t *testing.T) {
	skills := []Skill{{Name: "deploy", ArgumentHint: "dev|stage|prod"}}
	before := skills[0]
	_ = SkillArgumentHints(skills)
	if !reflect.DeepEqual(skills[0], before) {
		t.Errorf("input mutated: got %#v, want %#v", skills[0], before)
	}
}

func TestParseArgumentHint(t *testing.T) {
	text := "---\nname: deploy\nargument-hint: \"[dev|stage|prod]\"\n---\nbody"
	skill, err := Parse(text, filepath.Join("tmp", "deploy", "SKILL.md"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if skill.ArgumentHint != "[dev|stage|prod]" {
		t.Errorf("argument-hint = %q", skill.ArgumentHint)
	}
	hints := SkillArgumentHints([]Skill{skill})
	want := []string{"dev", "stage", "prod"}
	if got := hints["deploy"]; !reflect.DeepEqual(got, want) {
		t.Errorf("hints[deploy] = %v, want %v", got, want)
	}

	// 未加引号的 [...] 会被 frontmatter 解析器规范化为裸的逗号连接标量（括号被去掉），但候选项仍从 '|' 分隔符中恢复。
	// 逗号连接的标量（括号被去掉），但候选项仍
	// 从 '|' 分隔符中恢复。
	bare, err := Parse("---\nname: bare\nargument-hint: [dev|stage|prod]\n---\nbody", "bare.md")
	if err != nil {
		t.Fatalf("parse bare: %v", err)
	}
	if got := SkillArgumentHints([]Skill{bare})["bare"]; !reflect.DeepEqual(got, want) {
		t.Errorf("unquoted hints[bare] = %v, want %v", got, want)
	}
}

func TestArgumentHintCandidatesMultipleTokens(t *testing.T) {
	// 每个以空白分隔的标记单独检查；仅因另一个标记是
	// 真实枚举，就把包裹中的单个值泄漏进来是不允许的。
	// 每个以空白分隔的标记单独检查；仅因另一个标记是真实枚举，就把包裹中的单个值泄漏进来是不允许的。
	hint := "env dev|stage <path> x|y|z"
	got := argumentHintCandidates(hint)
	want := []string{"dev", "stage", "x", "y", "z"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("argumentHintCandidates(%q) = %v, want %v", hint, got, want)
	}
}
