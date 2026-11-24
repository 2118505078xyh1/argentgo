package extension

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/commands"
)

func TestDiscoverSkillsProjectsToDescriptors(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "demo")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: demo\ndescription: a demo skill\n---\nDo the thing.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	inv := DiscoverSkills([]string{dir})
	if len(inv.Descriptors) != 1 {
		t.Fatalf("descriptors = %+v, want 1", inv.Descriptors)
	}
	d := inv.Descriptors[0]
	if d.Kind != KindSkill || d.Name != "demo" || d.Path == "" || d.Trust != TrustUnknown {
		t.Fatalf("descriptor = %+v", d)
	}
}

func TestDiscoverCommandsProjectsToDescriptors(t *testing.T) {
	dir := t.TempDir()
	body := "---\ndescription: hello command\n---\nSay hello.\n"
	if err := os.WriteFile(filepath.Join(dir, "hello.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	inv := DiscoverCommands([]commands.Source{{Root: dir}})
	found := false
	for _, d := range inv.Descriptors {
		if d.Name == "hello" && d.Kind == KindCommand {
			found = true
		}
	}
	if !found {
		t.Fatalf("command descriptor not found in %+v", inv.Descriptors)
	}
}

func TestDiscoverPluginsEmptyRoot(t *testing.T) {
	inv, err := DiscoverPlugins(t.TempDir())
	if err != nil {
		t.Fatalf("DiscoverPlugins: %v", err)
	}
	if len(inv.Descriptors) != 0 || len(inv.Diagnostics) != 0 {
		t.Fatalf("empty root inventory = %+v", inv)
	}
}

func TestFromPluginProblemsAndHookStrings(t *testing.T) {
	if got := fromHookStrings("src", []string{"bad"}); len(got) != 1 || got[0].Message != "bad" {
		t.Fatalf("hook diagnostics = %+v", got)
	}
}
