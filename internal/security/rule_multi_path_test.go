package security

import "testing"

// TestRuleDenyOnPathsArrayNotMaskedByAskOnScalarPath reproduces the
// multi-resource aggregation gap behind the "ask masks deny" symptom: a
// path-based deny rule aimed at an entry of the checkpoint `paths` array used
// to be invisible to ruleDecision, which only scanned scalar path arguments.
// An ask rule matching a different, scalar path then won even though a deny
// rule covered another resource of the very same call. Deny must be absolute
// regardless of which argument carries the resource.
func TestRuleDenyOnPathsArrayNotMaskedByAskOnScalarPath(t *testing.T) {
	policy := NewPolicy(t.TempDir(), PolicyConfig{Rules: []Rule{
		{Decision: Ask, Tool: "write", Path: "safe.txt"},
		{Decision: Deny, Tool: "write", Path: "**/secret/**"},
	}})
	// "safe.txt" is a scalar `path`; "secret/key" is an entry of the `paths`
	// array. Both are resources of one call, so the deny must win.
	args := mustArgs(t, `{"path":"safe.txt","paths":["secret/key"]}`)
	if got := policy.Check("write", args); got != Deny {
		t.Fatalf("got %v, want deny: a deny rule on a paths-array entry must not be masked by an ask on a scalar path", got)
	}
}

// TestRuleDenyPathMatchesPathsArrayEntry covers the simpler case: a path rule
// must apply to every path resource, including the `paths` array used by
// checkpoint restores. Before the fix, a deny rule whose pattern only matched
// an array entry was skipped and the mutating tool fell back to its Ask.
func TestRuleDenyPathMatchesPathsArrayEntry(t *testing.T) {
	policy := NewPolicy(t.TempDir(), PolicyConfig{Rules: []Rule{
		{Decision: Deny, Tool: "write", Path: "**/secret/**"},
	}})
	args := mustArgs(t, `{"paths":["docs/secret/key.txt"]}`)
	if got := policy.Check("write", args); got != Deny {
		t.Fatalf("got %v, want deny: path rules must match entries of the paths array too", got)
	}
}

// TestRuleAskPathMatchesPathsArrayEntry guards the non-deny direction so the
// fix does not accidentally special-case only deny rules: an ask rule aimed at
// an array entry must also be honoured when no scalar path matches.
func TestRuleAskPathMatchesPathsArrayEntry(t *testing.T) {
	policy := NewPolicy(t.TempDir(), PolicyConfig{Rules: []Rule{
		{Decision: Ask, Tool: "read", Path: "**/secret/**"},
	}})
	// A read-only tool defaults to Allow, so an Ask here can only come from
	// the rule matching the array entry (not from the tool-kind fallback).
	args := mustArgs(t, `{"paths":["docs/secret/key.txt"]}`)
	if got := policy.Check("read", args); got != Ask {
		t.Fatalf("got %v, want ask: path rules must match entries of the paths array too", got)
	}
}
