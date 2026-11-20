package app

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/config"
	"github.com/2118505078xyh1/argentgo/internal/mcp"
	"github.com/2118505078xyh1/argentgo/internal/security"
	"github.com/2118505078xyh1/argentgo/internal/session"
)

// readOnlyMCPResolved builds a Resolved with one fake MCP server advertising a
// read-only tool and one ordinary tool. Extra config keys (permissions, etc.)
// are layered on top so the merge and deny cases share the same harness.
func readOnlyMCPResolved(root string, extra map[string]any) Resolved {
	raw := map[string]any{
		"mcpServers": map[string]any{
			"svc": map[string]any{"command": "fake-server"},
		},
	}
	for key, value := range extra {
		raw[key] = value
	}
	return Resolved{
		Root:       root,
		Cfg:        config.Config{Raw: raw},
		WriteRoots: security.WriteRootsFrom(root, nil),
		Sandbox:    security.SandboxWorkspaceWrite,
	}
}

func readOnlyMCPFactory(_ context.Context, _ mcp.Spec) (mcp.Client, error) {
	return &fakeMCPClient{tools: []mcp.Tool{
		{Name: "ro", ReadOnly: true},
		{Name: "rw", ReadOnly: false},
	}}, nil
}

func newReadOnlyMCPToolset(t *testing.T, res Resolved, chain *security.Chain) *toolset {
	t.Helper()
	store, err := session.Open(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.NewSession("readonly")
	if err != nil {
		t.Fatal(err)
	}
	set := newToolset(context.Background(), cli.IO{Env: envFrom(nil), Err: &bytes.Buffer{}}, res, sess, chain)
	t.Cleanup(func() { _ = set.Close() })
	return set
}

// TestMCPBindingPreservesReadOnlyHint pins the discarded-annotation bug: the
// parsed readOnlyHint must survive into the binding, and an unannotated tool
// must stay ordinary.
func TestMCPBindingPreservesReadOnlyHint(t *testing.T) {
	root := t.TempDir()
	res := readOnlyMCPResolved(root, nil)
	useFakeMCP(t, readOnlyMCPFactory)
	set := newReadOnlyMCPToolset(t, res, nil)

	seen := make(map[string]bool)
	for _, binding := range set.mcp {
		switch binding.spec.Function.Name {
		case "svc__ro":
			seen["ro"] = true
			if !binding.readOnly {
				t.Error("readOnlyHint=true MCP tool lost its read-only flag")
			}
		case "svc__rw":
			seen["rw"] = true
			if binding.readOnly {
				t.Error("unannotated MCP tool was marked read-only")
			}
		}
	}
	if !seen["ro"] || !seen["rw"] {
		t.Fatalf("bindings = %v, want svc__ro and svc__rw", specNames(set.Specs()))
	}
}

// TestMCPReadOnlyHintSkipsApproval is the end-to-end fix: after discovery the
// read-only tool is granted by the chain without ever asking the operator,
// while an ordinary tool still goes through the approval path.
func TestMCPReadOnlyHintSkipsApproval(t *testing.T) {
	root := t.TempDir()
	res := readOnlyMCPResolved(root, nil)
	approver := &countingApprover{grant: false}
	chain := security.NewChain(policyFor(res), approver, nil, "s1")
	useFakeMCP(t, readOnlyMCPFactory)
	set := newReadOnlyMCPToolset(t, res, chain)
	ctx := context.Background()

	if _, err := set.Run(ctx, "svc__ro", `{}`); err != nil {
		t.Fatalf("read-only MCP tool should run without approval: %v", err)
	}
	if approver.calls != 0 {
		t.Fatalf("operator asked %d times for a read-only MCP tool", approver.calls)
	}

	if _, err := set.Run(ctx, "svc__rw", `{}`); err == nil {
		t.Fatal("ordinary MCP tool should still require approval")
	}
	if approver.calls != 1 {
		t.Fatalf("operator asked %d times, want 1 for the ordinary tool", approver.calls)
	}
}

// TestMCPReadOnlyMergesWithConfiguredReadOnlyTools checks that runtime MCP
// names and the config-file readOnlyTools list coexist in the policy.
func TestMCPReadOnlyMergesWithConfiguredReadOnlyTools(t *testing.T) {
	root := t.TempDir()
	res := readOnlyMCPResolved(root, map[string]any{
		"permissions": map[string]any{
			"readOnlyTools": []any{"other__configured"},
		},
	})
	chain := security.NewChain(policyFor(res), &countingApprover{grant: false}, nil, "s1")
	useFakeMCP(t, readOnlyMCPFactory)
	newReadOnlyMCPToolset(t, res, chain)

	for _, name := range []string{"svc__ro", "other__configured"} {
		if got := chain.Policy.Check(name, json.RawMessage(`{}`)); got != security.Allow {
			t.Errorf("%s decision = %v, want allow", name, got)
		}
	}
	if got := chain.Policy.Check("svc__rw", json.RawMessage(`{}`)); got != security.Ask {
		t.Errorf("svc__rw decision = %v, want ask", got)
	}
}

// TestMCPReadOnlyDoesNotOverrideDeny keeps the policy priority: an explicit
// deny still wins over a readOnlyHint annotation.
func TestMCPReadOnlyDoesNotOverrideDeny(t *testing.T) {
	root := t.TempDir()
	res := readOnlyMCPResolved(root, map[string]any{
		"permissions": map[string]any{
			"denyTools": []any{"svc__ro"},
		},
	})
	approver := &countingApprover{grant: true}
	chain := security.NewChain(policyFor(res), approver, nil, "s1")
	useFakeMCP(t, readOnlyMCPFactory)
	set := newReadOnlyMCPToolset(t, res, chain)

	if _, err := set.Run(context.Background(), "svc__ro", `{}`); err == nil {
		t.Fatal("explicit deny must override readOnlyHint")
	}
	if got := chain.Policy.Check("svc__ro", json.RawMessage(`{}`)); got != security.Deny {
		t.Fatalf("svc__ro decision = %v, want deny", got)
	}
	if approver.calls != 0 {
		t.Fatalf("denied tool reached the operator (%d asks)", approver.calls)
	}
}

func TestMergeReadOnlyToolsDedupesAndTrims(t *testing.T) {
	got := mergeReadOnlyTools(
		[]string{" a ", "b", "a"},
		[]string{"", " b ", "c", "a"},
	)
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mergeReadOnlyTools = %q, want %q", got, want)
	}
}
