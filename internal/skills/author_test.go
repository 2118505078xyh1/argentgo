package skills

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestAuthorValidAndInvalidNames(t *testing.T) {
	valid := []string{"deploy", "my-skill", "v2-api", "a", "a1-b2-c3"}
	for _, name := range valid {
		t.Run("valid/"+name, func(t *testing.T) {
			path, err := Author(t.TempDir(), name, "desc", "body")
			if err != nil {
				t.Fatalf("Author(%q): %v", name, err)
			}
			if filepath.Base(filepath.Dir(path)) != name || filepath.Base(path) != "SKILL.md" {
				t.Errorf("path = %q", path)
			}
		})
	}

	invalid := []string{"", "Deploy", "my_skill", "../escape", "a/../b", ".", "..", "-lead", "trail-", "a--b", "a.b", "a/b", strings.Repeat("x", maxNameLength+1)}
	for _, name := range invalid {
		t.Run("invalid/"+name, func(t *testing.T) {
			if _, err := Author(t.TempDir(), name, "desc", "body"); !errors.Is(err, ErrInvalidSkillName) {
				t.Errorf("Author(%q) error = %v, want ErrInvalidSkillName", name, err)
			}
		})
	}
}

func TestAuthorWritesDiscoverableSkill(t *testing.T) {
	root := t.TempDir()
	path, err := Author(root, "deploy", "Ship the service", "Step one.\nStep two.")
	if err != nil {
		t.Fatalf("Author: %v", err)
	}
	if !filepath.IsAbs(path) {
		t.Errorf("path = %q, want absolute", path)
	}
	want := filepath.Join(root, filepath.FromSlash(DefaultProjectSubdir), "deploy", "SKILL.md")
	if path != want {
		t.Errorf("path = %q, want %q", path, want)
	}

	registry := Discover([]string{filepath.Join(root, filepath.FromSlash(DefaultProjectSubdir))})
	skill, err := registry.Load("deploy")
	if err != nil {
		t.Fatalf("Discover/Load: %v", err)
	}
	if skill.Description != "Ship the service" || skill.Body != "Step one.\nStep two." {
		t.Errorf("skill = %+v", skill)
	}
	if skill.Path != want {
		t.Errorf("skill path = %q, want %q", skill.Path, want)
	}
}

func TestAuthorRefusesOverwriteUnlessOptedIn(t *testing.T) {
	root := t.TempDir()
	if _, err := Author(root, "review", "first", "v1"); err != nil {
		t.Fatalf("first Author: %v", err)
	}
	if _, err := Author(root, "review", "second", "v2"); !errors.Is(err, ErrSkillExists) {
		t.Fatalf("second Author error = %v, want ErrSkillExists", err)
	}

	res, err := AuthorWithOptions(root, "review", "second", "v2", AuthorOptions{Overwrite: true})
	if err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	if res.Path == "" || res.Name != "review" {
		t.Errorf("result = %+v", res)
	}
	got, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "name: review") || !strings.Contains(string(got), "v2") {
		t.Errorf("overwritten file = %q", got)
	}
	if strings.Contains(string(got), "first") {
		t.Errorf("old description survived overwrite: %q", got)
	}
}

func TestAuthorEscapesFrontmatter(t *testing.T) {
	root := t.TempDir()
	description := `He said "it's fine" — C:\temp \n and --- in frontmatter`
	body := "1. do the thing\n2. note: --- is fine in a body\n"
	path, err := Author(root, "escape-test", description, body)
	if err != nil {
		t.Fatalf("Author: %v", err)
	}
	text, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(text), "---") < 2 {
		t.Fatalf("missing frontmatter fences: %q", text)
	}

	skill, err := Parse(string(text), path)
	if err != nil {
		t.Fatalf("Parse authored file: %v", err)
	}
	if skill.Name != "escape-test" {
		t.Errorf("name = %q", skill.Name)
	}
	if skill.Description != description {
		t.Errorf("description = %q, want %q", skill.Description, description)
	}
	if skill.Body != strings.TrimSpace(body) {
		t.Errorf("body = %q, want %q", skill.Body, strings.TrimSpace(body))
	}
}

func TestAuthorFoldsDescriptionNewlines(t *testing.T) {
	root := t.TempDir()
	path, err := Author(root, "fold", "first line\nsecond line\r\nthird", "body")
	if err != nil {
		t.Fatalf("Author: %v", err)
	}
	text, _ := os.ReadFile(path)
	skill, err := Parse(string(text), path)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if skill.Description != "first line second line third" {
		t.Errorf("description = %q", skill.Description)
	}
	if strings.Contains(string(text), "\nsecond line") {
		t.Errorf("raw newline leaked into frontmatter: %q", text)
	}
}

func TestAuthorEmptyRootUsesHomeSkills(t *testing.T) {
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
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("home skill not written: %v", err)
	}
}

func TestAuthorPublishIsSafeWithConcurrentDiscovery(t *testing.T) {
	root := t.TempDir()
	discoveryRoot := filepath.Join(root, filepath.FromSlash(DefaultProjectSubdir))
	var wg sync.WaitGroup
	errs := make(chan error, 2)

	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := Author(root, "concurrent", "concurrent skill", "body")
		errs <- err
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			// A partially written document would be skipped by Parse.
			Discover([]string{discoveryRoot})
		}
		errs <- nil
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Author/Discover: %v", err)
		}
	}
	registry := Discover([]string{discoveryRoot})
	if _, err := registry.Load("concurrent"); err != nil {
		t.Fatalf("final discovery: %v", err)
	}
}
