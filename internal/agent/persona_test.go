package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func writePersonaFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatalf("write persona %s: %v", name, err)
	}
}

func TestLoadPersonasReadsFrontmatterAndBody(t *testing.T) {
	dir := t.TempDir()
	writePersonaFile(t, dir, "reviewer.md", "---\nname: reviewer\nsubagent: true\ntools: read, grep\n---\nYou review code and report findings.\n")

	got, err := LoadPersonas(dir)
	if err != nil {
		t.Fatalf("LoadPersonas: %v", err)
	}
	persona, ok := got["reviewer"]
	if !ok {
		t.Fatalf("reviewer persona missing: %#v", got)
	}
	if persona.Name != "reviewer" {
		t.Errorf("Name = %q, want reviewer", persona.Name)
	}
	if persona.System != "You review code and report findings." {
		t.Errorf("System = %q, want markdown body", persona.System)
	}
	if want := []string{"read", "grep"}; !reflect.DeepEqual(persona.AllowedTools, want) {
		t.Errorf("AllowedTools = %#v, want %#v", persona.AllowedTools, want)
	}
}

func TestLoadPersonasInfersNameFromFilename(t *testing.T) {
	dir := t.TempDir()
	writePersonaFile(t, dir, "docs-writer.md", "---\nsubagent: true\n---\nWrite the docs.\n")

	got, err := LoadPersonas(dir)
	if err != nil {
		t.Fatalf("LoadPersonas: %v", err)
	}
	persona, ok := got["docs-writer"]
	if !ok {
		t.Fatalf("filename-derived persona missing: %#v", got)
	}
	if persona.Name != "docs-writer" {
		t.Errorf("Name = %q, want docs-writer", persona.Name)
	}
	if persona.System != "Write the docs." {
		t.Errorf("System = %q", persona.System)
	}
}

func TestLoadPersonasParsesToolsLists(t *testing.T) {
	dir := t.TempDir()
	writePersonaFile(t, dir, "comma.md", "---\nname: comma\nsubagent: true\ntools: read, \" write \" ,grep, read\n---\nbody\n")

	writePersonaFile(t, dir, "array.md", "---\nname: array\nsubagent: true\ntools: [\"read\", \"edit\", \"read\"]\n---\nbody\n")

	got, err := LoadPersonas(dir)
	if err != nil {
		t.Fatalf("LoadPersonas: %v", err)
	}
	if want := []string{"read", "write", "grep"}; !reflect.DeepEqual(got["comma"].AllowedTools, want) {
		t.Errorf("comma tools = %#v, want %#v", got["comma"].AllowedTools, want)
	}
	if want := []string{"read", "edit"}; !reflect.DeepEqual(got["array"].AllowedTools, want) {
		t.Errorf("array tools = %#v, want %#v", got["array"].AllowedTools, want)
	}
}

func TestLoadPersonasSkipsMalformedAndNonSubagentFiles(t *testing.T) {
	dir := t.TempDir()
	writePersonaFile(t, dir, "good.md", "---\nname: good\nsubagent: true\n---\ngood body\n")
	writePersonaFile(t, dir, "broken.md", "---\nname: broken\nthis line has no colon\nsubagent: true\n---\nbroken body\n")
	writePersonaFile(t, dir, "notes.md", "Just notes, no frontmatter.\n")
	writePersonaFile(t, dir, "plain.md", "---\nname: plain\nsubagent: false\n---\nnot a subagent\n")
	writePersonaFile(t, dir, "bad-tools.md", "---\nname: bad-tools\nsubagent: true\ntools: [not-json\n---\nbody\n")
	writePersonaFile(t, dir, "ignore.txt", "---\nname: ignore\nsubagent: true\n---\nbody\n")

	got, err := LoadPersonas(dir)
	if err == nil {
		t.Fatal("LoadPersonas: expected first malformed-file error")
	}
	if len(got) != 1 {
		t.Fatalf("got %d personas, want only the good one: %#v", len(got), got)
	}
	if persona, ok := got["good"]; !ok || persona.System != "good body" {
		t.Errorf("good persona not loaded correctly: %#v", got)
	}
}

func TestLoadPersonasEmptyOrMissingDir(t *testing.T) {
	empty := t.TempDir()
	got, err := LoadPersonas(empty)
	if err != nil || len(got) != 0 {
		t.Errorf("empty dir: got %#v, %v; want empty map and nil error", got, err)
	}

	got, err = LoadPersonas(filepath.Join(empty, "absent"))
	if err != nil || len(got) != 0 {
		t.Errorf("missing dir: got %#v, %v; want empty map and nil error", got, err)
	}
}

func TestLoadPersonasCapsFileCountAndSize(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < maxPersonaFiles; i++ {
		writePersonaFile(t, dir, "persona-"+strings.Repeat("x", 3)+"-"+strconv.Itoa(i)+".md", "---\nsubagent: true\n---\nbody\n")
	}
	writePersonaFile(t, dir, "overflow.md", "---\nsubagent: true\n---\nbody\n")

	got, err := LoadPersonas(dir)
	if err == nil {
		t.Fatal("expected a limit error for the overflow file")
	}
	if len(got) != maxPersonaFiles {
		t.Errorf("got %d personas, want cap of %d", len(got), maxPersonaFiles)
	}

	big := t.TempDir()
	if err := os.WriteFile(filepath.Join(big, "huge.md"), []byte(strings.Repeat("x", maxPersonaBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = LoadPersonas(big)
	if err == nil || len(got) != 0 {
		t.Errorf("oversize file: got %#v, %v; want empty map and error", got, err)
	}
}

func TestOrchestratorPromptHasWaveRules(t *testing.T) {
	prompt := OrchestratorPrompt()
	for _, phrase := range []string{"wave", "same files", "parallel", "final", "consolidated"} {
		if !strings.Contains(strings.ToLower(prompt), phrase) {
			t.Errorf("OrchestratorPrompt missing %q", phrase)
		}
	}
	if strings.HasSuffix(prompt, "\n") {
		t.Error("OrchestratorPrompt should be trimmed")
	}
}

func TestPersonaSubagentConfigMapsFields(t *testing.T) {
	persona := Persona{
		Name:         "reviewer",
		System:       "Review the code.",
		AllowedTools: []string{"read", "grep"},
	}
	cfg := persona.SubagentConfig()
	if cfg.System != persona.System {
		t.Errorf("System = %q, want %q", cfg.System, persona.System)
	}
	if !reflect.DeepEqual(cfg.AllowedTools, persona.AllowedTools) {
		t.Errorf("AllowedTools = %#v, want %#v", cfg.AllowedTools, persona.AllowedTools)
	}
	if cfg.Model != "" || cfg.MaxDepth != 0 || cfg.MaxTurns != 0 || cfg.Budget != (Budget{}) || cfg.OnEvent != nil {
		t.Errorf("SubagentConfig set unrelated fields: %#v", cfg)
	}

	// 适配器不得与角色的切片共享底层数组。
	cfg.AllowedTools[0] = "edit"
	if persona.AllowedTools[0] != "read" {
		t.Error("SubagentConfig aliased Persona.AllowedTools")
	}
}
