package plugins

import (
	"fmt"
	"strings"
)

// IssueKind classifies a PluginIssue so callers can group, filter or count
// findings without parsing message text.
type IssueKind string

const (
	// IssueEmptyField reports a required field that is blank.
	IssueEmptyField IssueKind = "empty_field"
	// IssueInvalidName reports an unsafe or malformed plugin name.
	IssueInvalidName IssueKind = "invalid_name"
	// IssueInvalidVersion reports a version that is not semantic.
	IssueInvalidVersion IssueKind = "invalid_version"
	// IssuePathEscape reports a declared path that resolves outside the bundle.
	IssuePathEscape IssueKind = "path_escape"
	// IssueDuplicateSource reports two declarations naming the same source.
	IssueDuplicateSource IssueKind = "duplicate_source"
	// IssueIncompleteMCP reports an MCP server with neither command nor url.
	IssueIncompleteMCP IssueKind = "incomplete_mcp"
)

// PluginIssue is one structural finding from ValidateManifest. It differs from
// Problem in two ways: it is produced by inspecting a Manifest only (it never
// touches the filesystem), and it carries a Field and Kind for programmatic
// use. Findings about files that do not exist belong to Manifest.Validate.
type PluginIssue struct {
	Field   string    `json:"field,omitempty"`
	Kind    IssueKind `json:"kind"`
	Message string    `json:"message"`
}

// String renders an issue for logs.
func (i PluginIssue) String() string {
	if i.Field != "" {
		return fmt.Sprintf("%s: %s", i.Field, i.Message)
	}
	return i.Message
}

// ValidateManifest reports structural problems with a parsed manifest: missing
// required fields, name and version format, paths that escape the bundle and
// duplicate MCP/skill/command sources.
//
// It is pure: paths are resolved against m.Root but no file is opened. Use
// Manifest.Validate for findings that require checking the filesystem (missing
// or wrongly-typed files). Findings are returned in a stable order: name,
// version, skills, commands, hooks, then MCP servers in sorted name order.
func ValidateManifest(m Manifest) []PluginIssue {
	var issues []PluginIssue
	add := func(field string, kind IssueKind, format string, args ...any) {
		issues = append(issues, PluginIssue{
			Field:   field,
			Kind:    kind,
			Message: fmt.Sprintf(format, args...),
		})
	}

	// Required fields. A blank value is reported once as a missing field; a
	// non-blank value is checked for legality, so the two never double up.
	if strings.TrimSpace(m.Name) == "" {
		add("name", IssueEmptyField, "name is empty")
	} else if err := ValidateName(m.Name); err != nil {
		add("name", IssueInvalidName, "%v", err)
	}
	if strings.TrimSpace(m.Version) == "" {
		add("version", IssueEmptyField, "version is empty")
	} else if err := ValidateVersion(m.Version); err != nil {
		add("version", IssueInvalidVersion, "version %q: %v", m.Version, err)
	}

	// Declared paths: a path that leaves the bundle is a fatal wiring problem,
	// and two spellings of one source ("skills", "./skills", "sub/../skills")
	// must be flagged so a manifest author can collapse them.
	checkPaths := func(field string, paths []string) {
		seen := make(map[string]string, len(paths))
		for _, raw := range paths {
			resolved := m.ResolvePath(raw)
			if !within(m.Root, resolved) {
				add(field, IssuePathEscape, "%s path %q escapes the bundle", field, raw)
				continue
			}
			if first, ok := seen[resolved]; ok {
				add(field, IssueDuplicateSource, "%s source %q duplicates %q", field, raw, first)
				continue
			}
			seen[resolved] = raw
		}
	}
	checkPaths("skills", m.Skills)
	checkPaths("commands", m.Commands)
	checkPaths("hooks", m.Hooks.Paths)

	// MCP servers. The map cannot hold two identical names, but two names may
	// point at the same command or url, which is almost always a copy/paste
	// slip worth surfacing before the wiring is built.
	bySource := make(map[string]string)
	for _, name := range sortedServerNames(m.MCPServers) {
		spec := m.MCPServers[name]
		field := "mcpServers." + name
		if strings.TrimSpace(spec.Command) == "" && strings.TrimSpace(spec.URL) == "" {
			add(field, IssueIncompleteMCP, "mcp server %q has neither command nor url", name)
			continue
		}
		source := spec.URL
		if source == "" {
			source = spec.Command + "\x00" + strings.Join(spec.Args, "\x00")
		}
		if first, ok := bySource[source]; ok {
			add(field, IssueDuplicateSource, "mcp server %q uses the same source as %q", name, first)
			continue
		}
		bySource[source] = name
	}
	return issues
}
