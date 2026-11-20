package skills

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sample = `---
name: deploy
description: "Ship the service"
when_to_use: >
  Use this when the user asks to deploy
  or roll back a release.
tools:
  - deploy = ./scripts/deploy.sh $ARGS
  - status = ./scripts/status.sh
allowed-tools: [bash, read]
---
Step one.
Step two.
`

func TestParseFrontmatterFields(t *testing.T) {
	skill, err := Parse(sample, filepath.Join("/tmp", "deploy", "SKILL.md"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if skill.Name != "deploy" {
		t.Errorf("name = %q", skill.Name)
	}
	if skill.Description != "Ship the service" {
		t.Errorf("description = %q", skill.Description)
	}
	want := "Use this when the user asks to deploy or roll back a release."
	if skill.WhenToUse != want {
		t.Errorf("when_to_use = %q, want %q", skill.WhenToUse, want)
	}
	if len(skill.Tools) != 2 || !strings.Contains(skill.Tools[0], "deploy.sh") {
		t.Errorf("tools = %#v", skill.Tools)
	}
	if len(skill.AllowedTools) != 2 || skill.AllowedTools[0] != "bash" || skill.AllowedTools[1] != "read" {
		t.Errorf("allowed-tools = %#v", skill.AllowedTools)
	}
	if skill.Body != "Step one.\nStep two." {
		t.Errorf("body = %q", skill.Body)
	}
}

func TestFrontmatterQuotingBlocksAndEdgeCases(t *testing.T) {
	cases := []struct {
		name string
		text string
		key  string
		want string
	}{
		{
			name: "single quotes",
			text: "---\nname: 'quoted name'\n---\nbody",
			key:  "name",
			want: "quoted name",
		},
		{
			name: "double quotes with escape",
			text: "---\ndescription: \"line\\nbreak\"\n---\nbody",
			key:  "description",
			want: "line\nbreak",
		},
		{
			name: "literal block keeps newlines",
			text: "---\ndescription: |\n  first\n  second\n---\nbody",
			key:  "description",
			want: "first\nsecond",
		},
		{
			name: "folded block joins words",
			text: "---\ndescription: >-\n  first\n  second\n\n  third\nname: x\n---\nbody",
			key:  "description",
			want: "first second\nthird",
		},
		{
			name: "comments and blank lines ignored",
			text: "---\n# a comment\n\nname: x\ndescription: plain\n---\nbody",
			key:  "description",
			want: "plain",
		},
		{
			name: "colon inside value",
			text: "---\ndescription: use git: always\n---\nbody",
			key:  "description",
			want: "use git: always",
		},
		{
			name: "last duplicate wins",
			text: "---\nname: a\nname: b\n---\nbody",
			key:  "name",
			want: "b",
		},
		{
			name: "crlf fences",
			text: "---\r\nname: x\r\ndescription: crlf\r\n---\r\nbody",
			key:  "description",
			want: "crlf",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			skill, err := Parse(tc.text, filepath.Join("tmp", "x", "SKILL.md"))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			got := skill.Name
			if tc.key == "description" {
				got = skill.Description
			}
			if got != tc.want {
				t.Errorf("%s = %q, want %q", tc.key, got, tc.want)
			}
		})
	}
}

func TestSplitFrontmatterEdgeCases(t *testing.T) {
	if _, _, ok := SplitFrontmatter("just prose"); ok {
		t.Error("prose must not have frontmatter")
	}
	if _, _, ok := SplitFrontmatter("---\nname: x\n"); ok {
		t.Error("unterminated block must not count")
	}
	_, body, ok := SplitFrontmatter("---\n---\nbody")
	if !ok || !strings.Contains(body, "body") {
		t.Errorf("empty block should still split, body = %q", body)
	}
	_, body, ok = SplitFrontmatter("\ufeff---\nname: x\n---\nafter")
	if !ok || strings.TrimSpace(body) != "after" {
		t.Errorf("BOM should be tolerated, body = %q", body)
	}
	if _, _, ok := SplitFrontmatter("--- \nname: x\n---\ny\n"); ok {
		t.Error("fence with trailing space must not open a block")
	}
}

func TestParseFallsBackToDirectoryName(t *testing.T) {
	skill, err := Parse("---\ndescription: no name here\n---\nbody", filepath.Join("tmp", "my-skill", "SKILL.md"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if skill.Name != "my-skill" {
		t.Errorf("name = %q", skill.Name)
	}
}

func TestParseRejectsMissingFrontmatter(t *testing.T) {
	if _, err := Parse("just prose", filepath.Join("tmp", "x", "SKILL.md")); err == nil {
		t.Fatal("expected an error")
	}
}

func TestDiscoverProjectShadowsHome(t *testing.T) {
	project, home := t.TempDir(), t.TempDir()
	projectSkills := filepath.Join(project, ".argent", "skills")
	homeSkills := filepath.Join(home, ".argentgo", "skills")
	writeSkill(t, projectSkills, "review", "project review")
	writeSkill(t, homeSkills, "review", "home review")
	writeSkill(t, homeSkills, "deploy", "home deploy")

	registry := Discover(DefaultRoots(project, home))
	if registry.Len() != 2 {
		t.Fatalf("len = %d, want 2", registry.Len())
	}
	review, ok := registry.Get("review")
	if !ok {
		t.Fatal("review missing")
	}
	if review.Description != "project review" {
		t.Errorf("description = %q, project should win", review.Description)
	}
}

func writeSkill(t *testing.T, skillsRoot, name, description string) {
	t.Helper()
	dir := filepath.Join(skillsRoot, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	text := "---\nname: " + name + "\ndescription: " + description + "\nwhen_to_use: when the user needs " + name + "\n---\nbody of " + name
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestIndexAndLoad(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "deploy", "ship the service")
	registry := Discover([]string{root})

	index := registry.IndexText()
	if !strings.Contains(index, "- deploy: ship the service") {
		t.Errorf("index = %q", index)
	}
	skill, err := registry.Load("deploy")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if skill.Body != "body of deploy" {
		t.Errorf("body = %q", skill.Body)
	}
	if _, err := registry.Load("nope"); err == nil {
		t.Fatal("expected ErrNotFound")
	}
	if _, err := registry.Load("deplo"); err == nil || !strings.Contains(err.Error(), "deploy") {
		t.Errorf("error should suggest deploy: %v", err)
	}
}

func TestEmptyRegistryIndexIsEmpty(t *testing.T) {
	if text := New().IndexText(); text != "" {
		t.Errorf("index = %q", text)
	}
}

func TestIndexTextKeepsOneLinePerSkill(t *testing.T) {
	cases := []struct {
		name        string
		frontmatter string
		wantLine    string
	}{
		{
			name:        "literal block description",
			frontmatter: "---\nname: docs\ndescription: |\n  first line\n  second line\n---\nbody",
			wantLine:    "- docs: first line second line",
		},
		{
			name:        "quoted newline escape",
			frontmatter: "---\nname: docs\ndescription: \"line\\nbreak\"\n---\nbody",
			wantLine:    "- docs: line break",
		},
		{
			name:        "literal block when_to_use",
			frontmatter: "---\nname: docs\nwhen_to_use: |\n  load me\n  right now\n---\nbody",
			wantLine:    "- docs (when: load me right now)",
		},
		{
			name:        "both fields multi-line",
			frontmatter: "---\nname: docs\ndescription: |\n  one\n  two\nwhen_to_use: |\n  three\n  four\n---\nbody",
			wantLine:    "- docs: one two (when: three four)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			skill, err := Parse(tc.frontmatter, filepath.Join("tmp", "docs", "SKILL.md"))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			r := New()
			r.Add(skill)
			index := r.IndexText()
			if !strings.Contains(index, tc.wantLine) {
				t.Errorf("IndexText = %q, want it to contain %q", index, tc.wantLine)
			}
			// Header plus exactly one line for the skill.
			lines := strings.Split(strings.TrimSuffix(index, "\n"), "\n")
			if len(lines) != 2 {
				t.Errorf("IndexText has %d lines, want 2: %q", len(lines), index)
			}
		})
	}
}

func TestMatchRanksByKeywordOverlap(t *testing.T) {
	registry := New()
	registry.Add(Skill{Name: "deploy", Description: "ship the service", WhenToUse: "release deploy"})
	registry.Add(Skill{Name: "review", Description: "review a diff", WhenToUse: "code review"})

	matches := registry.Match("please deploy the release")
	if len(matches) != 1 || matches[0].Name != "deploy" {
		t.Fatalf("matches = %#v", matches)
	}
	if matches[0].Score <= 0 {
		t.Errorf("score = %d", matches[0].Score)
	}
	if got := registry.Match("the and a"); len(got) != 0 {
		t.Errorf("stopwords matched: %#v", got)
	}
	if got := registry.Match(""); len(got) != 0 {
		t.Errorf("empty prompt matched: %#v", got)
	}
}

func TestUseSkillSpecIsValidJSON(t *testing.T) {
	spec := UseSkillSpec()
	if spec.Function.Name != "use_skill" {
		t.Errorf("name = %q", spec.Function.Name)
	}
	var schema map[string]any
	if err := json.Unmarshal(spec.Function.Parameters, &schema); err != nil {
		t.Fatalf("parameters: %v", err)
	}
	if schema["type"] != "object" {
		t.Errorf("schema type = %v", schema["type"])
	}
}

func TestHandleUseSkill(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "deploy", "ship it")
	registry := Discover([]string{root})

	list, err := registry.HandleUseSkill(json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(list, "deploy") {
		t.Errorf("list = %q", list)
	}

	body, err := registry.HandleUseSkill(json.RawMessage(`{"name":"deploy"}`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !strings.Contains(body, "# skill: deploy") || !strings.Contains(body, "body of deploy") {
		t.Errorf("body = %q", body)
	}

	if _, err := registry.HandleUseSkill(json.RawMessage(`{"name":"missing"}`)); err == nil {
		t.Fatal("expected an error for an unknown skill")
	}
	if _, err := registry.HandleUseSkill(json.RawMessage(`{`)); err == nil {
		t.Fatal("expected an error for invalid JSON")
	}
}

func TestHandleUseSkillListKeepsOneLinePerSkill(t *testing.T) {
	r := New()
	r.Add(Skill{Name: "docs", Description: "first\nsecond", Path: "/tmp/docs/SKILL.md"})
	list, err := r.HandleUseSkill(json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if want := "- docs — first second\n  /tmp/docs/SKILL.md\n"; !strings.Contains(list, want) {
		t.Errorf("list = %q, want it to contain %q", list, want)
	}
	if strings.Contains(list, "\nsecond") {
		t.Errorf("multi-line description leaked: %q", list)
	}
}
