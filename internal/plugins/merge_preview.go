package plugins

import (
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// ConflictKind identifies the contribution category a MergeConflict refers to.
type ConflictKind string

const (
	ConflictMCPServer ConflictKind = "mcp_server"
	ConflictSkill     ConflictKind = "skill"
	ConflictCommand   ConflictKind = "command"
	ConflictHook      ConflictKind = "hook"
)

// Contribution is one item a manifest would add to the merged wiring.
type Contribution struct {
	// Plugin is the name of the declaring manifest.
	Plugin string
	// Name is the collision key: an MCP server key, or the base name of a
	// skill/command/hook path (downstream loaders register these by name).
	Name string
	// Source is the MCP command (or URL) for servers, and the resolved path
	// for skills, commands and hooks.
	Source string
}

// MergeConflict is a name claimed by more than one manifest. Owners holds the
// declaring plugin names in input order.
type MergeConflict struct {
	Kind   ConflictKind
	Name   string
	Owners []string
}

// MergePreview is the dry-run result of PreviewMerge. Each slice keeps the
// input order of manifests and, within a manifest, declaration order (MCP
// servers use sorted name order to match Merge). Conflicts use a fixed kind
// order and sorted names so the value is stable and comparable.
type MergePreview struct {
	MCPServers []Contribution
	Skills     []Contribution
	Commands   []Contribution
	Hooks      []Contribution
	Conflicts  []MergeConflict
}

// claim records which manifest contributed a name. ownerIndex keeps two
// distinct manifests that share the same Name from collapsing into one owner.
type claim struct {
	ownerIndex int
	ownerName  string
}

// PreviewMerge reports what the given manifests would contribute when merged,
// and which names are claimed by more than one of them. It is a pure dry run:
// paths are resolved against each manifest's Root, but no file is opened, no
// server is started and nothing is written to disk.
//
// Merge only ever keeps the first MCP server with a given name and dedups
// skill/command/hook paths by their resolved form; it never surfaces that two
// bundles competed for the same registry name. PreviewMerge makes those
// cross-manifest collisions visible before the wiring is built.
func PreviewMerge(manifests []Manifest) MergePreview {
	var preview MergePreview
	claims := map[ConflictKind]map[string][]claim{
		ConflictMCPServer: {},
		ConflictSkill:     {},
		ConflictCommand:   {},
		ConflictHook:      {},
	}
	addClaim := func(kind ConflictKind, name string, index int, owner string) {
		claims[kind][name] = append(claims[kind][name], claim{ownerIndex: index, ownerName: owner})
	}

	for index, manifest := range manifests {
		owner := manifest.Name
		if owner == "" {
			owner = filepath.Base(filepath.Clean(manifest.Path))
		}
		if owner == "" || owner == "." {
			owner = "unnamed"
		}

		for _, serverName := range sortedServerNames(manifest.MCPServers) {
			spec := manifest.MCPServers[serverName]
			source := spec.Command
			if source == "" {
				source = spec.URL
			}
			preview.MCPServers = append(preview.MCPServers, Contribution{
				Plugin: owner, Name: serverName, Source: source,
			})
			addClaim(ConflictMCPServer, serverName, index, owner)
		}

		groups := []struct {
			kind  ConflictKind
			paths []string
			dest  *[]Contribution
		}{
			{ConflictSkill, manifest.Skills, &preview.Skills},
			{ConflictCommand, manifest.Commands, &preview.Commands},
			{ConflictHook, manifest.Hooks.Paths, &preview.Hooks},
		}
		for _, group := range groups {
			for _, relative := range group.paths {
				resolved := manifest.ResolvePath(relative)
				name := path.Base(strings.ReplaceAll(resolved, "\\", "/"))
				if name == "." || name == "/" {
					name = resolved
				}
				*group.dest = append(*group.dest, Contribution{
					Plugin: owner, Name: name, Source: resolved,
				})
				addClaim(group.kind, name, index, owner)
			}
		}
	}

	for _, kind := range []ConflictKind{ConflictMCPServer, ConflictSkill, ConflictCommand, ConflictHook} {
		byName := claims[kind]
		names := make([]string, 0, len(byName))
		for name := range byName {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			forName := byName[name]
			// A manifest repeating a name is a ValidateManifest finding, not a
			// cross-manifest conflict, so count distinct source manifests only.
			byIndex := make(map[int]bool, len(forName))
			owners := make([]string, 0, len(forName))
			for _, entry := range forName {
				if byIndex[entry.ownerIndex] {
					continue
				}
				byIndex[entry.ownerIndex] = true
				owners = append(owners, entry.ownerName)
			}
			if len(byIndex) < 2 {
				continue
			}
			preview.Conflicts = append(preview.Conflicts, MergeConflict{
				Kind: kind, Name: name, Owners: owners,
			})
		}
	}
	return preview
}
