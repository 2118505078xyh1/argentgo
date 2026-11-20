package app

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/security"
	"github.com/2118505078xyh1/argentgo/internal/skills"
)

// resetSharedDefs installs a fresh DefinitionCache singleton for a test. The
// discovery call sites use a package-level singleton (there is no App/Factory
// receiver to hang a field on), so a test that asserts cache accounting needs
// to start from a clean one. This helper does not call t.Parallel; the only
// parallel tests in this package exercise the runner and never touch defs.
func resetSharedDefs(t *testing.T) *DefinitionCache {
	t.Helper()
	defsOnce = sync.Once{}
	c := sharedDefs()
	t.Cleanup(func() {
		defsOnce = sync.Once{}
		defs = nil
	})
	return c
}

// writeWireSkill lays down one discoverable SKILL.md in a directory, using an
// isolated workspace root so the fingerprint only reflects that root.
func writeWireSkill(t *testing.T, root, name string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(skills.DefaultProjectSubdir), name, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndescription: ship it\n---\nStep one.\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestWiredDefinitionsCacheDedupesSystemBuilds proves the wiring: two system
// prompts built from the same roots run discovery once, Refresh forces a
// rediscovery, and the skill still appears in the prompt (no behaviour change).
func TestWiredDefinitionsCacheDedupesSystemBuilds(t *testing.T) {
	env, root, _ := wiringEnv(t)
	writeWireSkill(t, root, "deploy")

	c := resetSharedDefs(t)
	if h, m, r := c.Stats(); h != 0 || m != 0 || r != 0 {
		t.Fatalf("fresh cache stats = (%d,%d,%d), want (0,0,0)", h, m, r)
	}

	prompt1 := buildSystemWithTrust(root, env, true, "", "")
	prompt2 := buildSystemWithTrust(root, env, true, "", "")
	if prompt1 != prompt2 {
		t.Fatal("two builds from the same roots produced different prompts")
	}
	if !strings.Contains(prompt1, "deploy") {
		t.Fatalf("system prompt lost the discovered skill:\n%s", prompt1)
	}
	assertDefStats(t, c, 1, 1, 0)

	c.Refresh()
	prompt3 := buildSystemWithTrust(root, env, true, "", "")
	if !strings.Contains(prompt3, "deploy") {
		t.Fatalf("skill missing after Refresh:\n%s", prompt3)
	}
	assertDefStats(t, c, 1, 2, 1)
}

// TestWiredDefinitionsCacheSharedAcrossCallSites proves buildSystemWithTrust
// and contextPrompt share one snapshot: the second site is served from the
// cache rather than rescanning the same skill roots.
func TestWiredDefinitionsCacheSharedAcrossCallSites(t *testing.T) {
	env, root, _ := wiringEnv(t)
	writeWireSkill(t, root, "deploy")

	c := resetSharedDefs(t)
	buildSystemWithTrust(root, env, true, "", "")
	assertDefStats(t, c, 0, 1, 0)

	res := Resolved{Root: root, Sandbox: security.SandboxReadOnly, Approval: "ask"}
	report, ok := contextPrompt(res, cli.IO{Env: env}, "/context")
	if !ok {
		t.Fatal("/context was not routed to the built-in hook")
	}
	if !strings.Contains(report, "skills: 1") {
		t.Fatalf("context report did not count the cached skill:\n%s", report)
	}
	assertDefStats(t, c, 1, 1, 0)
}
