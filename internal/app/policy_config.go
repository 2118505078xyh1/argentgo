package app

import (
	"path/filepath"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

// policyFor is the single construction point for the policy used by Execute.
// Keeping it separate makes the enforcement of configured rules testable
// without a model or a network.
func policyFor(res Resolved) *security.Policy {
	return security.NewPolicy(res.Root, policyConfigFrom(res.Root, res.Cfg.Raw, res.WriteRoots, res.Sandbox))
}

// policyConfigFrom decodes the user-facing permission keys of the merged config
// tree into a security.PolicyConfig. Keys may be camelCase or snake_case and
// may live at the top level or inside "permissions"; a nested key wins.
//
// The resolved sandbox is merged last: read-only adds denies for every mutating
// tool, while danger-full-access removes the write-root clipping but leaves
// explicit user rules untouched.
func policyConfigFrom(root string, raw map[string]any, writeRoots []string, sandbox security.SandboxMode) security.PolicyConfig {
	lookup := policyLookup(raw)
	var cfg security.PolicyConfig
	cfg.DenyPaths = policyStrings(lookup("denyPaths", "deny_paths"))
	cfg.AllowPaths = policyStrings(lookup("allowPaths", "allow_paths"))
	cfg.DenyCommands = policyStrings(lookup("denyCommands", "deny_commands"))
	cfg.DenyCommandPaths = policyStrings(lookup("denyCommandPaths", "deny_command_paths"))
	cfg.DenyTools = policyStrings(lookup("denyTools", "deny_tools"))
	cfg.AllowTools = policyStrings(lookup("allowTools", "allow_tools"))
	cfg.ReadOnlyTools = policyStrings(lookup("readOnlyTools", "read_only_tools"))
	cfg.MutatingTools = policyStrings(lookup("mutatingTools", "mutating_tools"))
	cfg.Rules = policyRules(lookup("rules"))

	// The resolved mode is authoritative; the raw "sandbox" key is still read
	// so a config-only setting is never silently ignored.
	configured := policyString(lookup("sandbox"))
	readOnly := sandbox == security.SandboxReadOnly || strings.EqualFold(configured, "read-only")
	fullAccess := sandbox == security.SandboxDangerFullAccess && !readOnly
	if fullAccess {
		cfg.WriteRoots = unrestrictedWriteRoots(root)
	} else {
		cfg.WriteRoots = append([]string(nil), writeRoots...)
		cfg.WriteRoots = append(cfg.WriteRoots, policyStrings(lookup("writeRoots", "write_roots"))...)
	}
	if readOnly {
		cfg.DenyTools = appendMutatingDenies(cfg.DenyTools, cfg.MutatingTools)
	}
	return cfg
}

// policyLookup prefers the permissions object, then the top level, and checks
// every spelling in each scope before moving on.
func policyLookup(raw map[string]any) func(...string) any {
	var nested map[string]any
	for _, key := range []string{"permissions", "Permissions"} {
		if value, ok := raw[key].(map[string]any); ok {
			nested = value
			break
		}
	}
	return func(keys ...string) any {
		for _, key := range keys {
			if nested != nil {
				if value, ok := nested[key]; ok {
					return value
				}
			}
		}
		for _, key := range keys {
			if value, ok := raw[key]; ok {
				return value
			}
		}
		return nil
	}
}

// policyStrings accepts JSON arrays, comma-separated strings and already
// decoded []string values, matching security.PolicyFromJSON's leniency.
func policyStrings(value any) []string {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				if text = strings.TrimSpace(text); text != "" {
					out = append(out, text)
				}
			}
		}
		return out
	case string:
		out := make([]string, 0, 4)
		for _, item := range strings.Split(typed, ",") {
			if item = strings.TrimSpace(item); item != "" {
				out = append(out, item)
			}
		}
		return out
	}
	return nil
}

func policyString(value any) string {
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return ""
}

func policyRules(value any) []security.Rule {
	var items []any
	switch typed := value.(type) {
	case []any:
		items = typed
	case []map[string]any:
		for _, item := range typed {
			items = append(items, item)
		}
	case []security.Rule:
		return append([]security.Rule(nil), typed...)
	}
	rules := make([]security.Rule, 0, len(items))
	for _, item := range items {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		decision, ok := security.ParseDecision(policyStringField(entry, "decision", "action"))
		if !ok {
			continue
		}
		rules = append(rules, security.Rule{
			Decision: decision,
			Tool:     policyStringField(entry, "tool"),
			Path:     policyStringField(entry, "path"),
			Command:  policyStringField(entry, "command"),
		})
	}
	return rules
}

func policyStringField(entry map[string]any, keys ...string) string {
	for _, key := range keys {
		if text, ok := entry[key].(string); ok {
			return strings.TrimSpace(text)
		}
	}
	return ""
}

// unrestrictedWriteRoots returns a root that contains every path on the
// current volume (or the whole tree on unix), so write-boundary checks become
// a no-op while every explicit deny/path rule still applies.
func unrestrictedWriteRoots(root string) []string {
	return []string{filepath.VolumeName(root) + string(filepath.Separator)}
}

var builtinMutatingToolNames = []string{"write", "edit", "multi_edit", "bash"}

// appendMutatingDenies makes read-only mode explicit in the policy as well as
// in the tool registry, so MCP and plugin tools cannot mutate either.
func appendMutatingDenies(deny, extra []string) []string {
	seen := make(map[string]bool, len(deny)+len(extra)+len(builtinMutatingToolNames))
	out := make([]string, 0, len(deny)+len(extra)+len(builtinMutatingToolNames))
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, name)
	}
	for _, name := range deny {
		add(name)
	}
	for _, name := range builtinMutatingToolNames {
		add(name)
	}
	for _, name := range extra {
		add(name)
	}
	return out
}
