package app

import (
	"strings"
	"testing"
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
