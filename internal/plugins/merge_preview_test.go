package plugins

import (
	"path/filepath"
	"reflect"
	"testing"
)

// TestPreviewMergeEmpty checks that no manifests produce an empty preview with
// no contributions and no conflicts, for both nil and non-nil empty slices.
func TestPreviewMergeEmpty(t *testing.T) {
	for _, input := range [][]Manifest{nil, {}} {
		got := PreviewMerge(input)
		want := MergePreview{}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("PreviewMerge(%v) = %+v, want empty preview", input, got)
		}
	}
}

// TestPreviewMergeAggregatesContributions checks that several manifests are
// flattened in input order, each contribution carrying its owner, collision
// name and resolved source, and that independent names raise no conflict.
func TestPreviewMergeAggregatesContributions(t *testing.T) {
	root := filepath.Join(t.TempDir(), "bundles")
	alpha := Manifest{
		Name:       "alpha",
		Root:       filepath.Join(root, "alpha"),
		MCPServers: map[string]MCPServer{"fs": {Command: "npx", URL: ""}},
		Skills:     []string{"skills/write.md"},
		Commands:   []string{"commands/open.md"},
		Hooks:      Hooks{Paths: []string{"hooks.json"}},
	}
	beta := Manifest{
		Name:       "beta",
		Root:       filepath.Join(root, "beta"),
		MCPServers: map[string]MCPServer{"search": {URL: "http://localhost:9"}},
		Skills:     []string{"skills/read.md"},
		Commands:   []string{"commands/close.md"},
		Hooks:      Hooks{Paths: []string{"hooks/hook.json"}},
	}

	got := PreviewMerge([]Manifest{alpha, beta})

	wantServers := []Contribution{
		{Plugin: "alpha", Name: "fs", Source: "npx"},
		{Plugin: "beta", Name: "search", Source: "http://localhost:9"},
	}
	wantSkills := []Contribution{
		{Plugin: "alpha", Name: "write.md", Source: filepath.Join(alpha.Root, "skills", "write.md")},
		{Plugin: "beta", Name: "read.md", Source: filepath.Join(beta.Root, "skills", "read.md")},
	}
	wantCommands := []Contribution{
		{Plugin: "alpha", Name: "open.md", Source: filepath.Join(alpha.Root, "commands", "open.md")},
		{Plugin: "beta", Name: "close.md", Source: filepath.Join(beta.Root, "commands", "close.md")},
	}
	wantHooks := []Contribution{
		{Plugin: "alpha", Name: "hooks.json", Source: filepath.Join(alpha.Root, "hooks.json")},
		{Plugin: "beta", Name: "hook.json", Source: filepath.Join(beta.Root, "hooks", "hook.json")},
	}
	if !reflect.DeepEqual(got.MCPServers, wantServers) {
		t.Fatalf("MCPServers = %+v, want %+v", got.MCPServers, wantServers)
	}
	if !reflect.DeepEqual(got.Skills, wantSkills) {
		t.Fatalf("Skills = %+v, want %+v", got.Skills, wantSkills)
	}
	if !reflect.DeepEqual(got.Commands, wantCommands) {
		t.Fatalf("Commands = %+v, want %+v", got.Commands, wantCommands)
	}
	if !reflect.DeepEqual(got.Hooks, wantHooks) {
		t.Fatalf("Hooks = %+v, want %+v", got.Hooks, wantHooks)
	}
	if len(got.Conflicts) != 0 {
		t.Fatalf("Conflicts = %+v, want none for distinct names", got.Conflicts)
	}
}

// TestPreviewMergeDetectsCrossManifestConflicts checks each contribution kind
// for a name claimed by two different manifests, and that a single manifest
// repeating a name is not misreported as a cross-manifest conflict.
func TestPreviewMergeDetectsCrossManifestConflicts(t *testing.T) {
	tests := []struct {
		name       string
		first      Manifest
		second     Manifest
		wantKind   ConflictKind
		wantName   string
		wantOwners []string
	}{
		{
			name:       "mcp server key",
			first:      Manifest{Name: "alpha", MCPServers: map[string]MCPServer{"shared": {Command: "a"}}},
			second:     Manifest{Name: "beta", MCPServers: map[string]MCPServer{"shared": {Command: "b"}}},
			wantKind:   ConflictMCPServer,
			wantName:   "shared",
			wantOwners: []string{"alpha", "beta"},
		},
		{
			name:       "skill base name in different folders",
			first:      Manifest{Name: "alpha", Skills: []string{"a/deploy.md"}},
			second:     Manifest{Name: "beta", Skills: []string{"b/deploy.md"}},
			wantKind:   ConflictSkill,
			wantName:   "deploy.md",
			wantOwners: []string{"alpha", "beta"},
		},
		{
			name:       "command base name",
			first:      Manifest{Name: "alpha", Commands: []string{"cmd/run.md"}},
			second:     Manifest{Name: "beta", Commands: []string{"other/run.md"}},
			wantKind:   ConflictCommand,
			wantName:   "run.md",
			wantOwners: []string{"alpha", "beta"},
		},
		{
			name:       "hook base name",
			first:      Manifest{Name: "alpha", Hooks: Hooks{Paths: []string{"a/hooks.json"}}},
			second:     Manifest{Name: "beta", Hooks: Hooks{Paths: []string{"b/hooks.json"}}},
			wantKind:   ConflictHook,
			wantName:   "hooks.json",
			wantOwners: []string{"alpha", "beta"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := PreviewMerge([]Manifest{tc.first, tc.second})
			want := []MergeConflict{{Kind: tc.wantKind, Name: tc.wantName, Owners: tc.wantOwners}}
			if !reflect.DeepEqual(got.Conflicts, want) {
				t.Fatalf("Conflicts = %+v, want %+v", got.Conflicts, want)
			}
		})
	}

	// One manifest listing the same skill base name twice is a within-manifest
	// issue, not a conflict between manifests.
	single := PreviewMerge([]Manifest{{
		Name:   "alpha",
		Skills: []string{"a/deploy.md", "b/deploy.md"},
	}})
	if len(single.Conflicts) != 0 {
		t.Fatalf("single-manifest Conflicts = %+v, want none", single.Conflicts)
	}
}

// TestPreviewMergeCJKNames checks that Chinese plugin names, MCP server keys
// and path base names are preserved intact and participate in conflict
// detection (ValidateName accepts CJK letters).
func TestPreviewMergeCJKNames(t *testing.T) {
	first := Manifest{
		Name:       "文件插件",
		Root:       filepath.Join("bundle", "甲"),
		MCPServers: map[string]MCPServer{"文件系统": {Command: "server"}},
		Skills:     []string{"技能/说明.md"},
	}
	second := Manifest{
		Name:       "搜索插件",
		Root:       filepath.Join("bundle", "乙"),
		MCPServers: map[string]MCPServer{"搜索": {URL: "http://localhost:9"}},
		Skills:     []string{"工具/说明.md"},
	}

	got := PreviewMerge([]Manifest{first, second})

	wantServers := []Contribution{
		{Plugin: "文件插件", Name: "文件系统", Source: "server"},
		{Plugin: "搜索插件", Name: "搜索", Source: "http://localhost:9"},
	}
	wantSkills := []Contribution{
		{Plugin: "文件插件", Name: "说明.md", Source: filepath.Join(first.Root, "技能", "说明.md")},
		{Plugin: "搜索插件", Name: "说明.md", Source: filepath.Join(second.Root, "工具", "说明.md")},
	}
	if !reflect.DeepEqual(got.MCPServers, wantServers) {
		t.Fatalf("MCPServers = %+v, want %+v", got.MCPServers, wantServers)
	}
	if !reflect.DeepEqual(got.Skills, wantSkills) {
		t.Fatalf("Skills = %+v, want %+v", got.Skills, wantSkills)
	}
	wantConflicts := []MergeConflict{{Kind: ConflictSkill, Name: "说明.md", Owners: []string{"文件插件", "搜索插件"}}}
	if !reflect.DeepEqual(got.Conflicts, wantConflicts) {
		t.Fatalf("Conflicts = %+v, want %+v", got.Conflicts, wantConflicts)
	}
}
