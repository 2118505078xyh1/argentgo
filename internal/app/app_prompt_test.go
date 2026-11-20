package app

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/config"
)

// TestStaticSystemIgnoresProcessContext proves the cache-stable prefix does not
// embed process-specific state. Two builds from different working directories
// (with no project documents or skills that would legitimately differ) must be
// byte-identical, and neither may mention the cwd or the reasoning effort.
func TestStaticSystemIgnoresProcessContext(t *testing.T) {
	env, rootA, _ := wiringEnv(t)
	rootB := t.TempDir()

	staticA := buildStaticSystem(rootA, env, true)
	staticB := buildStaticSystem(rootB, env, true)
	if staticA != staticB {
		t.Fatalf("static system drifted with cwd:\nA: %q\nB: %q", staticA, staticB)
	}
	for _, leaked := range []string{"Working directory", "Reasoning effort", "JSON Schema"} {
		if strings.Contains(staticA, leaked) {
			t.Fatalf("static system leaked process context %q: %q", leaked, staticA)
		}
	}
}

// TestDynamicContextCarriesProcessContext proves no information is lost by the
// split: the dynamic half still carries the cwd, the reasoning effort and the
// output schema, using the exact wording of the original combined prompt.
func TestDynamicContextCarriesProcessContext(t *testing.T) {
	root := t.TempDir()
	const schema = `{"type":"object","properties":{"ok":{"type":"boolean"}}}`
	dynamic := buildDynamicContext(root, "high", schema)
	for _, want := range []string{
		"Working directory: " + root,
		"Reasoning effort: high. Spend accordingly.",
		"Reply with a single JSON document that conforms to this JSON Schema:",
		schema,
	} {
		if !strings.Contains(dynamic, want) {
			t.Fatalf("dynamic context missing %q: %q", want, dynamic)
		}
	}
}

// TestCachePrefixStableAcrossProcessChanges proves the bytes a provider caches
// (the static system prefix) stay the same when the cwd or thinking level
// changes, while the dynamic values still reach the request through the
// composed prompt. This is the invariant the dsh-style split relies on.
func TestCachePrefixStableAcrossProcessChanges(t *testing.T) {
	env, rootA, _ := wiringEnv(t)
	rootB := t.TempDir()

	prefixA := buildStaticSystem(rootA, env, true)
	prefixB := buildStaticSystem(rootB, env, true)
	fullA := buildSystemWithTrust(rootA, env, true, "low", "")
	fullB := buildSystemWithTrust(rootB, env, true, "high", "")

	if prefixA != prefixB {
		t.Fatalf("cache prefix changed across cwd/thinking:\nA: %q\nB: %q", prefixA, prefixB)
	}
	for _, full := range []string{fullA, fullB} {
		if !strings.HasPrefix(full, prefixA) {
			t.Fatalf("combined system no longer starts with the static prefix:\nfull: %q\nprefix: %q", full, prefixA)
		}
	}
	if !strings.Contains(fullA, "Reasoning effort: low") || !strings.Contains(fullB, "Reasoning effort: high") {
		t.Fatalf("dynamic reasoning effort lost from combined prompt:\nA: %q\nB: %q", fullA, fullB)
	}
	if !strings.Contains(fullB, "Working directory: "+rootB) {
		t.Fatalf("dynamic cwd lost from combined prompt: %q", fullB)
	}
}

// TestBuildSystemWithTrustKeepsDynamicInfo guards the legacy composition: the
// full system string used by existing callers must still contain every field
// after the refactor, so no existing prompt assertion changes meaning.
func TestBuildSystemWithTrustKeepsDynamicInfo(t *testing.T) {
	env, root, _ := wiringEnv(t)
	const schema = `{"type":"object"}`
	full := buildSystemWithTrust(root, env, true, "medium", schema)
	for _, want := range []string{
		"Working directory: " + root,
		"Reasoning effort: medium. Spend accordingly.",
		"Reply with a single JSON document that conforms to this JSON Schema:",
		schema,
	} {
		if !strings.Contains(full, want) {
			t.Fatalf("combined system missing %q: %q", want, full)
		}
	}
}

// TestPlatformFactsInStaticSystem proves the cache-stable prefix carries the
// runtime OS/architecture and the inferred shell, in both the static-only and
// the composed builders. COMSPEC uses a bare base name so the assertion holds
// on every host OS.
func TestPlatformFactsInStaticSystem(t *testing.T) {
	_, root, home := wiringEnv(t)
	factsEnv := envFrom(map[string]string{
		"USERPROFILE": home,
		"HOME":        home,
		"COMSPEC":     "cmd.exe",
	})
	wantPlatform := fmt.Sprintf("Platform: %s/%s", runtime.GOOS, runtime.GOARCH)
	for name, system := range map[string]string{
		"static":   buildStaticSystem(root, factsEnv, true),
		"composed": buildSystem(root, factsEnv, "", ""),
	} {
		for _, want := range []string{wantPlatform, "Shell: cmd.exe"} {
			if !strings.Contains(system, want) {
				t.Errorf("%s system missing %q: %q", name, want, system)
			}
		}
	}
}

// TestShellFallsBackToSHELL proves COMSPEC is not required: on Unix the SHELL
// variable supplies the shell name.
func TestShellFallsBackToSHELL(t *testing.T) {
	env := envFrom(map[string]string{"SHELL": "bash"})
	system := buildStaticSystem(t.TempDir(), env, true)
	if !strings.Contains(system, "Shell: bash") {
		t.Fatalf("static system missing SHELL fallback: %q", system)
	}
}

// TestVerifyGuidanceOnlyWhenConfigured proves the default prompt bytes are
// unchanged by verification config: absent when nothing is configured, and
// present with a do-not-repeat hint once either field is set.
func TestVerifyGuidanceOnlyWhenConfigured(t *testing.T) {
	env, root, _ := wiringEnv(t)

	if base := buildSystem(root, env, "", ""); strings.Contains(base, "Verification:") {
		t.Fatalf("default prompt leaked verify guidance: %q", base)
	}

	withCmd := buildSystem(root, env, "", "", config.Config{
		VerifyCommand: []string{"go", "test", "./..."},
	})
	for _, want := range []string{"Verification:", "go test ./...", "do not suggest re-running them"} {
		if !strings.Contains(withCmd, want) {
			t.Errorf("VerifyCommand prompt missing %q: %q", want, withCmd)
		}
	}

	withMap := buildSystem(root, env, "", "", config.Config{
		VerifyCommands: map[string]string{".go": "go vet ./...", "py": "pytest -q"},
	})
	for _, want := range []string{"go vet ./...", "pytest -q", "do not suggest re-running them"} {
		if !strings.Contains(withMap, want) {
			t.Errorf("VerifyCommands prompt missing %q: %q", want, withMap)
		}
	}
}

// TestVerifyGuidanceDeterministic proves map iteration order cannot make the
// cache-stable prefix drift: two configs with identical entries inserted in
// different orders must render byte-identical prompts.
func TestVerifyGuidanceDeterministic(t *testing.T) {
	env, root, _ := wiringEnv(t)
	a := buildSystem(root, env, "", "", config.Config{
		VerifyCommands: map[string]string{".go": "go test ./...", "py": "pytest -q"},
	})
	b := buildSystem(root, env, "", "", config.Config{
		VerifyCommands: map[string]string{"py": "pytest -q", ".go": "go test ./..."},
	})
	if a != b {
		t.Fatalf("verify guidance drifted with map order:\nA: %q\nB: %q", a, b)
	}
}

// TestCWDGitDetection proves the repository hint follows the workspace root:
// a .git directory or .git file marks it, a plain directory does not.
func TestCWDGitDetection(t *testing.T) {
	env, _, _ := wiringEnv(t)

	plain := t.TempDir()
	if got := buildSystem(plain, env, "", ""); strings.Contains(got, "Repository:") {
		t.Fatalf("plain directory wrongly marked as a repository: %q", got)
	}
	const want = "Repository: this working directory is a git repository."

	dirRepo := t.TempDir()
	if err := os.Mkdir(filepath.Join(dirRepo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := buildSystem(dirRepo, env, "", ""); !strings.Contains(got, want) {
		t.Fatalf(".git directory not detected: %q", got)
	}

	fileRepo := t.TempDir()
	if err := os.WriteFile(filepath.Join(fileRepo, ".git"), []byte("gitdir: elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := buildSystem(fileRepo, env, "", ""); !strings.Contains(got, want) {
		t.Fatalf(".git file not detected: %q", got)
	}
}
