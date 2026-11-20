package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseFrontmatterFields(t *testing.T) {
	text := `---
description: Review a diff
argument-hint: <path> [focus]
allowed-tools: bash, read
model: gpt-5
---
Review $ARGUMENTS carefully.`
	cmd := Parse(text, filepath.Join("tmp", "review.md"))
	if cmd.Name != "review" || cmd.Description != "Review a diff" {
		t.Errorf("name/description = %q/%q", cmd.Name, cmd.Description)
	}
	if cmd.ArgumentHint != "<path> [focus]" {
		t.Errorf("argument-hint = %q", cmd.ArgumentHint)
	}
	if len(cmd.AllowedTools) != 2 || cmd.AllowedTools[0] != "bash" {
		t.Errorf("allowed-tools = %#v", cmd.AllowedTools)
	}
	if cmd.Model != "gpt-5" {
		t.Errorf("model = %q", cmd.Model)
	}
	if cmd.Body != "Review $ARGUMENTS carefully." {
		t.Errorf("body = %q", cmd.Body)
	}
}

func TestParseStrictCRLFFrontmatter(t *testing.T) {
	// A .md saved with Windows CRLF line endings must parse exactly like LF.
	text := "---\r\n" +
		"description: Review a diff\r\n" +
		"allowed-tools: bash, read\r\n" +
		"model: gpt-5\r\n" +
		"---\r\n" +
		"Review $ARGUMENTS carefully.\r\n"
	cmd, err := ParseStrict(text, filepath.Join("tmp", "review.md"))
	if err != nil {
		t.Fatalf("ParseStrict: %v", err)
	}
	if cmd.Description != "Review a diff" {
		t.Errorf("description = %q", cmd.Description)
	}
	if len(cmd.AllowedTools) != 2 || cmd.AllowedTools[0] != "bash" || cmd.AllowedTools[1] != "read" {
		t.Errorf("allowed-tools = %#v", cmd.AllowedTools)
	}
	if cmd.Model != "gpt-5" {
		t.Errorf("model = %q", cmd.Model)
	}
	if strings.Contains(cmd.Body, "description:") || strings.Contains(cmd.Body, "allowed-tools:") || strings.Contains(cmd.Body, "---") {
		t.Errorf("body leaked frontmatter: %q", cmd.Body)
	}
	if cmd.Body != "Review $ARGUMENTS carefully." {
		t.Errorf("body = %q", cmd.Body)
	}
}

func TestFileWithoutFrontmatterIsACommand(t *testing.T) {
	cmd := Parse("Deploy the site\n\nRun the script.", filepath.Join("tmp", "deploy.md"))
	if cmd.Name != "deploy" {
		t.Errorf("name = %q", cmd.Name)
	}
	if cmd.Description != "Deploy the site" {
		t.Errorf("description = %q", cmd.Description)
	}
	if !strings.Contains(cmd.Body, "Run the script.") {
		t.Errorf("body = %q", cmd.Body)
	}
}

func TestExpandArguments(t *testing.T) {
	cmd := Command{Name: "fix", Body: "fix $1 in $2; all: $ARGUMENTS; at: $@; braced: ${1}"}
	got, err := cmd.Expand("a.rs tests")
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	want := "fix a.rs in tests; all: a.rs tests; at: a.rs tests; braced: a.rs"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	empty, err := cmd.Expand("")
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if empty != "fix  in ; all: ; at: ; braced: " {
		t.Errorf("empty args = %q", empty)
	}
}

func TestExpandWithExtraWhitespace(t *testing.T) {
	cmd := Command{Name: "x", Body: "$1|$2"}
	got, _ := cmd.Expand("  one    two  three ")
	if got != "one|two" {
		t.Errorf("got %q", got)
	}
}

func TestProjectShadowsUserAndPluginNamespaced(t *testing.T) {
	project, home, plugin := t.TempDir(), t.TempDir(), t.TempDir()
	write(t, project, "review.md", "---\ndescription: project review\n---\nproject body")
	write(t, home, "review.md", "---\ndescription: user review\n---\nuser body")
	write(t, home, "test.md", "---\ndescription: user test\n---\ntest body")
	write(t, plugin, "review.md", "---\ndescription: plugin review\n---\nplugin body")

	set := Discover([]Source{
		{Root: project},
		{Root: home},
		{Root: plugin, Prefix: "acme"},
	})
	if set.Len() != 3 {
		t.Fatalf("len = %d, want 3: %#v", set.Len(), set.List())
	}
	review, ok := set.Get("review")
	if !ok || !strings.Contains(review.Body, "project") {
		t.Errorf("project command should win: %#v", review)
	}
	if _, ok := set.Get("test"); !ok {
		t.Error("user command missing")
	}
	namespaced, ok := set.Get("acme:review")
	if !ok {
		t.Fatal("namespaced plugin command missing")
	}
	if namespaced.Plugin != "acme" || !strings.Contains(namespaced.Body, "plugin") {
		t.Errorf("plugin command = %#v", namespaced)
	}
	if _, ok := set.Get("plugin:review"); ok {
		t.Error("unprefixed plugin name should not resolve")
	}
}

func write(t *testing.T, root, name, text string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestShellInterpolationIsOffByDefault(t *testing.T) {
	cmd := Command{Name: "x", Body: "head !`echo hi` tail $1"}
	got, err := cmd.Expand("arg")
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if got != "head !`echo hi` tail arg" {
		t.Errorf("got %q", got)
	}
}

func TestShellInterpolationWhenEnabled(t *testing.T) {
	cmd := Command{Name: "x", Body: "head !`git status` tail $1"}
	calls := 0
	got, err := cmd.ExpandWith("arg", ExpandOptions{
		AllowShell: true,
		Run: func(command string) (string, error) {
			calls++
			if command != "git status" {
				t.Errorf("command = %q", command)
			}
			return "clean\n", nil
		},
	})
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d", calls)
	}
	if got != "head clean tail arg" {
		t.Errorf("got %q", got)
	}
}

func TestShellInterpolationFailureIsReported(t *testing.T) {
	cmd := Command{Name: "x", Body: "!`boom`"}
	_, err := cmd.ExpandWith("", ExpandOptions{AllowShell: true, Run: func(string) (string, error) {
		return "", os.ErrPermission
	}})
	if err == nil {
		t.Fatal("expected an error")
	}
}

func TestSetExpandUnknownCommand(t *testing.T) {
	set := Discover(nil)
	if _, err := set.Expand("nope", ""); err == nil {
		t.Fatal("expected an error")
	}
	if len(set.List()) != 0 {
		t.Errorf("empty set list = %#v", set.List())
	}
}
