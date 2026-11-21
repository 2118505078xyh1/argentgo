package skills

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
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

// 可信度是调用方的职责。
// YAML 双引号标量从左到右处理转义："\\\\" 是单个字面反斜杠，因此后面的 "n" 或 "t" 是普通文本。链式
// ReplaceAll 调用会在 "\\\\" 之前处理 "\n"，把转义的
// ReplaceAll 调用会在 "\\\\" 之前处理 "\n"，把 "C:\\temp\\name" 这样的转义 Windows 路径破坏成制表符和换行。
func TestParseDoubleQuotedEscapesInOrder(t *testing.T) {
	cases := []struct {
		name, value, want string
	}{
		{"escaped backslash before n", `a\\nb`, `a\nb`},
		{"escaped backslash before t", `a\\tb`, `a\tb`},
		{"windows path stays literal", `C:\\temp\\name`, `C:\temp\name`},
		{"real escapes still work", `C:\temp\name`, "C:\temp\n" + "ame"},
		{"escaped quote", `a\"b`, `a"b`},
		{"tab escape", `a\tb`, "a\tb"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text := "---\ndescription: \"" + tc.value + "\"\n---\nbody"
			skill, err := Parse(text, filepath.Join("tmp", "x", "SKILL.md"))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if skill.Description != tc.want {
				t.Errorf("description = %q, want %q", skill.Description, tc.want)
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
			// 头部加恰好一行用于技能。
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

// Part 1：渲染边界必须先把声明性元数据脱敏再折叠为单行。

func TestIndexTextNameNewlineCannotInjectEntry(t *testing.T) {
	// 带引号的 YAML 标量在 Parse 时会把 \n 解码为真实换行，name 在 Parse 中不受校验。
	skill, err := Parse("---\nname: \"foo\\n- fake skill\"\ndescription: real\n---\nbody", filepath.Join("tmp", "x", "SKILL.md"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	originalName := skill.Name
	r := New()
	r.Add(skill)
	index := r.IndexText()

	lines := strings.Split(strings.TrimSuffix(index, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("index has %d lines, want header + 1: %q", len(lines), index)
	}
	for _, line := range lines {
		if strings.HasPrefix(line, "- fake") {
			t.Errorf("injected entry leaked: %q", line)
		}
	}
	if !strings.Contains(index, "- foo - fake skill: real") {
		t.Errorf("name was not folded into one line: %q", index)
	}
	if skill.Name != originalName {
		t.Errorf("IndexText mutated input: %q", skill.Name)
	}
	if again := r.IndexText(); again != index {
		t.Errorf("IndexText is unstable: %q vs %q", index, again)
	}
}

func TestIndexTextRedactsInvisibleCharacters(t *testing.T) {
	const tag = "\U000E0001"
	const bidi = "\u200e"
	skill := Skill{
		Name:        "docs" + tag,
		Description: "中文" + bidi + "说明",
		WhenToUse:   "load " + tag + " me",
	}
	before := skill
	r := New()
	r.Add(skill)
	index := r.IndexText()

	if strings.Contains(index, tag) {
		t.Errorf("invisible tag leaked: %q", index)
	}
	if strings.Contains(index, bidi) {
		t.Errorf("raw bidi character leaked: %q", index)
	}
	if !strings.Contains(index, `中文\u200e说明`) {
		t.Errorf("bidi should be visible as an escape: %q", index)
	}
	if !strings.Contains(index, "- docs: ") || !strings.Contains(index, "(when: load me)") {
		t.Errorf("tag characters were not redacted: %q", index)
	}
	if !reflect.DeepEqual(skill, before) {
		t.Errorf("IndexText mutated input: %#v", skill)
	}
	if again := r.IndexText(); again != index {
		t.Errorf("IndexText is unstable: %q vs %q", index, again)
	}
}

func TestHandleUseSkillListSanitizesName(t *testing.T) {
	r := New()
	r.Add(Skill{Name: "docs\n- fake", Description: "first\nsecond", Path: "/tmp/docs/SKILL.md"})
	list, err := r.HandleUseSkill(json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if strings.Contains(list, "\n- fake") {
		t.Errorf("injected entry leaked: %q", list)
	}
	if !strings.Contains(list, "- docs - fake — first second\n") {
		t.Errorf("name was not folded into one line: %q", list)
	}
}

// Part 2：多路径发现，写入目标与发现路径解耦。

func TestDefaultRootsOrder(t *testing.T) {
	cwd, home := filepath.Join("work", "proj"), filepath.Join("home", "user")
	want := []string{
		filepath.Join(cwd, ".argent", "skills"),
		filepath.Join(home, ".argentgo", "skills"),
		filepath.Join(home, ".agents", "skills"),
		filepath.Join(home, ".claude", "skills"),
	}
	if got := DefaultRoots(cwd, home); !reflect.DeepEqual(got, want) {
		t.Errorf("DefaultRoots = %q, want %q", got, want)
	}
	onlyProject := []string{filepath.Join(cwd, ".argent", "skills")}
	if got := DefaultRoots(cwd, ""); !reflect.DeepEqual(got, onlyProject) {
		t.Errorf("DefaultRoots with empty home = %q, want %q", got, onlyProject)
	}
}

func TestDiscoverSameNameAcrossFourRoots(t *testing.T) {
	project, home := t.TempDir(), t.TempDir()
	roots := DefaultRoots(project, home)
	writeSkill(t, roots[0], "review", "project review")
	writeSkill(t, roots[1], "review", "argentgo review")
	writeSkill(t, roots[2], "review", "agents review")
	writeSkill(t, roots[3], "review", "claude review")

	reg := Discover(roots)
	if reg.Len() != 1 {
		t.Fatalf("len = %d, want 1 (same name must not overwrite)", reg.Len())
	}
	got, ok := reg.Get("review")
	if !ok {
		t.Fatal("review missing")
	}
	if got.Description != "project review" {
		t.Errorf("description = %q, project version should win", got.Description)
	}

	// 用户目录之间同样按 DefaultUserSubdirs 的顺序决胜。
	for _, tc := range []struct {
		roots []string
		want  string
	}{
		{roots[1:], "argentgo review"},
		{roots[2:], "agents review"},
		{roots[3:], "claude review"},
	} {
		got, ok := Discover(tc.roots).Get("review")
		if !ok || got.Description != tc.want {
			t.Errorf("discovery from %q: description = %q, want %q", tc.roots, got.Description, tc.want)
		}
	}
}

func TestDiscoverKeepsValidSkillsBesideBrokenOnes(t *testing.T) {
	project, home := t.TempDir(), t.TempDir()
	roots := DefaultRoots(project, home)
	writeSkill(t, roots[2], "longform-memory", "memory from agents")
	brokenDir := filepath.Join(roots[3], "broken")
	if err := os.MkdirAll(brokenDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(brokenDir, "SKILL.md"), []byte("not a skill"), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := Discover(roots)
	if reg.Len() != 1 {
		t.Fatalf("len = %d, want 1", reg.Len())
	}
	if _, ok := reg.Get("longform-memory"); !ok {
		t.Fatal("valid skill in user directory was not discovered")
	}
}

func TestIndexTextOneLinePerExternalSkill(t *testing.T) {
	project, home := t.TempDir(), t.TempDir()
	roots := DefaultRoots(project, home)
	names := []string{"alpha", "beta", "gamma", "delta"}
	for i, name := range names {
		writeSkill(t, roots[i], name, "desc of "+name)
	}
	index := Discover(roots).IndexText()
	lines := strings.Split(strings.TrimSuffix(index, "\n"), "\n")
	if len(lines) != len(names)+1 {
		t.Fatalf("index has %d lines, want header + %d: %q", len(lines), len(names), index)
	}
	for _, line := range lines[1:] {
		if !strings.HasPrefix(line, "- ") {
			t.Errorf("unexpected index line: %q", line)
		}
	}
}

func TestIndexTextByteStableWithoutExternalDirs(t *testing.T) {
	project := t.TempDir()
	roots := DefaultRoots(project, "")
	writeSkill(t, roots[0], "deploy", "ship the service")
	const want = "Skills: procedures you can load by name with the use_skill tool. Load one when the task matches its description; do not load all of them.\n" +
		"- deploy: ship the service (when: when the user needs deploy)\n"
	if got := Discover(roots).IndexText(); got != want {
		t.Errorf("IndexText = %q, want %q", got, want)
	}
}

func TestAuthorWritesOnlyToLegacyHomeSubdir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)

	path, err := Author("", "home-skill", "home", "body")
	if err != nil {
		t.Fatalf("Author with empty root: %v", err)
	}
	want := filepath.Join(home, filepath.FromSlash(DefaultHomeSubdir), "home-skill", "SKILL.md")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	for _, subdir := range DefaultUserSubdirs[1:] {
		other := filepath.Join(home, filepath.FromSlash(subdir), "home-skill", "SKILL.md")
		if _, err := os.Stat(other); !os.IsNotExist(err) {
			t.Errorf("unexpected file under %s: %v", other, err)
		}
	}
}
