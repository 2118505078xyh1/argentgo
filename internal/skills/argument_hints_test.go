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

	// An unquoted [...] is normalised by the frontmatter parser into a bare
	// comma-joined scalar (the brackets are dropped), yet the candidates are
	// still recovered from the '|' separators.
	bare, err := Parse("---\nname: bare\nargument-hint: [dev|stage|prod]\n---\nbody", "bare.md")
	if err != nil {
		t.Fatalf("parse bare: %v", err)
	}
	if got := SkillArgumentHints([]Skill{bare})["bare"]; !reflect.DeepEqual(got, want) {
		t.Errorf("unquoted hints[bare] = %v, want %v", got, want)
	}
}

func TestArgumentHintCandidatesMultipleTokens(t *testing.T) {
	// Each whitespace-delimited token is inspected on its own; a single value
	// enclosed in a wrapper must not leak in just because another token is a
	// real enumeration.
	hint := "env dev|stage <path> x|y|z"
	got := argumentHintCandidates(hint)
	want := []string{"dev", "stage", "x", "y", "z"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("argumentHintCandidates(%q) = %v, want %v", hint, got, want)
	}
}
