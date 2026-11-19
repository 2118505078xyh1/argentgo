package tools_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/security"
	"github.com/2118505078xyh1/argentgo/internal/tools"
)

func specNames(specs []llm.ToolSpec) []string {
	names := make([]string, 0, len(specs))
	for _, spec := range specs {
		names = append(names, spec.Function.Name)
	}
	return names
}

func containsName(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

func TestSpecsForFiltersAdvertisedCatalog(t *testing.T) {
	root := t.TempDir()
	denyWebFetch := security.NewPolicy(root, security.PolicyConfig{DenyTools: []string{"web_fetch"}})
	allowTwo := security.NewPolicy(root, security.PolicyConfig{AllowTools: []string{"read", "ls"}})
	blanketDenyBash := security.NewPolicy(root, security.PolicyConfig{
		Rules: []security.Rule{{Decision: security.Deny, Tool: "bash"}},
	})
	askBash := security.NewPolicy(root, security.PolicyConfig{
		Rules: []security.Rule{{Decision: security.Ask, Tool: "bash"}},
	})
	pathDeny := security.NewPolicy(root, security.PolicyConfig{DenyPaths: []string{"**/.env"}})

	registry := tools.New(root, true)
	all := specNames(registry.Specs())

	cases := []struct {
		name    string
		policy  tools.CatalogPolicy
		mode    security.SandboxMode
		absent  []string
		present []string
		exact   []string
	}{
		{
			name:    "read-only drops mutating tools",
			mode:    security.SandboxReadOnly,
			absent:  []string{"write", "edit", "multi_edit", "bash"},
			present: []string{"read", "ls", "grep", "todo", "web_fetch"},
		},
		{
			name:    "deny rule drops only that tool",
			policy:  denyWebFetch,
			mode:    security.SandboxWorkspaceWrite,
			absent:  []string{"web_fetch"},
			present: []string{"read", "bash", "write"},
		},
		{
			name:   "allow list keeps only authorized tools",
			policy: allowTwo,
			mode:   security.SandboxWorkspaceWrite,
			exact:  []string{"read", "ls"},
		},
		{
			name:   "blanket deny rule drops the tool",
			policy: blanketDenyBash,
			mode:   security.SandboxWorkspaceWrite,
			absent: []string{"bash"},
			// A rule with no path/command is a permanent deny.
		},
		{
			name:    "ask rule keeps the tool advertised",
			policy:  askBash,
			mode:    security.SandboxWorkspaceWrite,
			present: []string{"bash"},
		},
		{
			name:    "path-only deny keeps the tool advertised",
			policy:  pathDeny,
			mode:    security.SandboxWorkspaceWrite,
			present: []string{"read", "write"},
		},
		{
			name:  "normal mode keeps every authorized tool",
			mode:  security.SandboxWorkspaceWrite,
			exact: all,
		},
		{
			name:    "full access keeps mutating tools",
			mode:    security.SandboxDangerFullAccess,
			present: []string{"write", "edit", "bash"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := specNames(registry.SpecsFor(tc.policy, tc.mode))
			if tc.exact != nil {
				if !reflect.DeepEqual(got, tc.exact) {
					t.Fatalf("specs = %v, want %v", got, tc.exact)
				}
			}
			for _, name := range tc.absent {
				if containsName(got, name) {
					t.Fatalf("%s was advertised despite the filter: %v", name, got)
				}
			}
			for _, name := range tc.present {
				if !containsName(got, name) {
					t.Fatalf("%s was filtered out: %v", name, got)
				}
			}
		})
	}
}

func TestSpecsForKeepsSpecMetadataAndOrder(t *testing.T) {
	registry := tools.New(t.TempDir(), true)
	want := registry.Specs()
	got := registry.SpecsFor(nil, security.SandboxWorkspaceWrite)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unfiltered specs = %v, want the registry catalog %v", got, want)
	}
	for _, spec := range got {
		if spec.Type != "function" || spec.Function.Name == "" ||
			spec.Function.Description == "" || !json.Valid(spec.Function.Parameters) {
			t.Fatalf("incomplete spec: %+v", spec)
		}
	}
}

func TestRunStillEnforcesDeniedToolAfterCatalogFilter(t *testing.T) {
	root := t.TempDir()
	policy := security.NewPolicy(root, security.PolicyConfig{DenyTools: []string{"bash"}})
	registry := tools.New(root, true)
	registry.SetGuard(&recordingDenyGuard{policy: policy})

	if containsName(specNames(registry.SpecsFor(policy, security.SandboxWorkspaceWrite)), "bash") {
		t.Fatal("denied tool was advertised")
	}
	if _, err := registry.Run(context.Background(), "bash", `{"command":"echo hi"}`); err == nil {
		t.Fatal("Run accepted a denied tool; the guard backstop is missing")
	}
}

type recordingDenyGuard struct {
	policy *security.Policy
}

func (g *recordingDenyGuard) Check(_ context.Context, name string, args json.RawMessage) error {
	if g.policy.Check(name, args) == security.Deny {
		return security.ErrDenied
	}
	return nil
}

func TestFilterSpecsHandlesCallerSuppliedTools(t *testing.T) {
	registry := tools.New(t.TempDir(), true)
	specs := []llm.ToolSpec{
		toolSpecFor("read"), toolSpecFor("write"), toolSpecFor("bash"), toolSpecFor("mcp__server__search"),
	}
	got := specNames(registry.FilterSpecs(nil, security.SandboxReadOnly, specs))
	want := []string{"read", "mcp__server__search"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("filtered specs = %v, want %v", got, want)
	}
}

func toolSpecFor(name string) llm.ToolSpec {
	var spec llm.ToolSpec
	spec.Type = "function"
	spec.Function.Name = name
	return spec
}
