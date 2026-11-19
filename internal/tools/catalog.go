package tools

import (
	"encoding/json"

	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/security"
)

// CatalogPolicy is the narrow slice of a security policy needed to decide
// whether a tool may be advertised. *security.Policy satisfies it. A nil
// CatalogPolicy means "no denies".
//
// Visibility is only a courtesy to the model: Run still consults the full
// guard chain, so a filtered-out tool that is somehow called is rejected
// there. The check uses empty arguments: denies that depend on a particular
// path or command cannot hide a tool that is legal with other arguments.
type CatalogPolicy interface {
	Check(name string, args json.RawMessage) security.Decision
}

// SpecsFor returns the tool schemas the model should see under policy and
// sandbox mode, in stable registration order. Read-only mode drops mutating
// tools; the policy drops tools that are denied unconditionally.
func (r *Registry) SpecsFor(policy CatalogPolicy, mode security.SandboxMode) []llm.ToolSpec {
	specs := make([]llm.ToolSpec, 0, len(r.order))
	for _, name := range r.order {
		if !r.ToolAllowed(policy, mode, name) {
			continue
		}
		tool := r.tools[name]
		spec := llm.ToolSpec{Type: "function"}
		spec.Function.Name = tool.Name()
		spec.Function.Description = tool.Description()
		spec.Function.Parameters = json.RawMessage(tool.Schema())
		specs = append(specs, spec)
	}
	return specs
}

// ToolAllowed reports whether name may be advertised under policy and mode.
// Unknown names are not registry tools and are therefore never advertised by
// SpecsFor; FilterSpecs applies a looser rule to caller-supplied specs.
func (r *Registry) ToolAllowed(policy CatalogPolicy, mode security.SandboxMode, name string) bool {
	if _, known := r.tools[name]; !known {
		return false
	}
	return visibilityAllowed(policy, mode, name, func() bool {
		return r.tools[name].Mutating()
	})
}

// FilterSpecs narrows any spec list (built-ins, MCP tools, injected skills) by
// the same policy and sandbox rules as SpecsFor. Names the registry does not
// know are checked against the policy and the built-in mutating-name table,
// so an MCP tool called "bash" cannot slip through in read-only mode.
func (r *Registry) FilterSpecs(policy CatalogPolicy, mode security.SandboxMode, specs []llm.ToolSpec) []llm.ToolSpec {
	out := make([]llm.ToolSpec, 0, len(specs))
	for _, spec := range specs {
		name := spec.Function.Name
		mutating := func() bool {
			if tool, known := r.tools[name]; known {
				return tool.Mutating()
			}
			return security.MutatingTool(name)
		}
		if visibilityAllowed(policy, mode, name, mutating) {
			out = append(out, spec)
		}
	}
	return out
}

// visibilityAllowed is the shared decision: read-only rejects anything known
// to mutate, and a policy deny with no arguments rejects the tool outright.
func visibilityAllowed(policy CatalogPolicy, mode security.SandboxMode, name string, mutating func() bool) bool {
	if mode == security.SandboxReadOnly && mutating() {
		return false
	}
	if policy != nil && policy.Check(name, nil) == security.Deny {
		return false
	}
	return true
}
