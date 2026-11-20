package plugins

import (
	"path/filepath"
	"strings"
	"testing"
)

// wantIssue is the observable part of an issue asserted by TestValidateManifest:
// the field it belongs to and its kind. Messages are human-facing text and are
// checked separately where it matters.
type wantIssue struct {
	field string
	kind  IssueKind
}

func TestValidateManifest(t *testing.T) {
	root := t.TempDir()
	outsideAbs := filepath.Join(root, "..", "outside-abs")

	tests := []struct {
		name   string
		mutate func(*Manifest)
		want   []wantIssue
	}{
		{
			name:   "valid CJK name and semantic version produce no issues",
			mutate: func(m *Manifest) {},
		},
		{
			name:   "missing but in-bounds paths are not flagged",
			mutate: func(m *Manifest) { m.Skills = []string{"does-not-exist"} },
		},
		{
			name:   "empty name is a missing field",
			mutate: func(m *Manifest) { m.Name = "" },
			want:   []wantIssue{{"name", IssueEmptyField}},
		},
		{
			name:   "whitespace version is a missing field",
			mutate: func(m *Manifest) { m.Version = "  \t " },
			want:   []wantIssue{{"version", IssueEmptyField}},
		},
		{
			name:   "name with a path separator is illegal",
			mutate: func(m *Manifest) { m.Name = "../evil" },
			want:   []wantIssue{{"name", IssueInvalidName}},
		},
		{
			name:   "reserved Windows device name is illegal",
			mutate: func(m *Manifest) { m.Name = "CON.txt" },
			want:   []wantIssue{{"name", IssueInvalidName}},
		},
		{
			name:   "non-semantic version is illegal",
			mutate: func(m *Manifest) { m.Version = "1.2" },
			want:   []wantIssue{{"version", IssueInvalidVersion}},
		},
		{
			name:   "relative skill path escaping the bundle is reported",
			mutate: func(m *Manifest) { m.Skills = []string{"../../outside"} },
			want:   []wantIssue{{"skills", IssuePathEscape}},
		},
		{
			name:   "absolute command path outside the bundle is reported",
			mutate: func(m *Manifest) { m.Commands = []string{outsideAbs} },
			want:   []wantIssue{{"commands", IssuePathEscape}},
		},
		{
			name:   "hook path escaping the bundle is reported",
			mutate: func(m *Manifest) { m.Hooks.Paths = []string{"../../hook.json"} },
			want:   []wantIssue{{"hooks", IssuePathEscape}},
		},
		{
			name:   "exact duplicate skill sources are reported",
			mutate: func(m *Manifest) { m.Skills = []string{"skills", "skills"} },
			want:   []wantIssue{{"skills", IssueDuplicateSource}},
		},
		{
			name:   "different spellings of one skill source are reported",
			mutate: func(m *Manifest) { m.Skills = []string{"skills", "./skills", "sub/../skills"} },
			want: []wantIssue{
				{"skills", IssueDuplicateSource},
				{"skills", IssueDuplicateSource},
			},
		},
		{
			name:   "duplicate command sources are reported",
			mutate: func(m *Manifest) { m.Commands = []string{"commands/a.md", "commands/./a.md"} },
			want:   []wantIssue{{"commands", IssueDuplicateSource}},
		},
		{
			name:   "duplicate hook sources are reported",
			mutate: func(m *Manifest) { m.Hooks.Paths = []string{"h.json", "h.json"} },
			want:   []wantIssue{{"hooks", IssueDuplicateSource}},
		},
		{
			name: "two MCP servers with the same command are reported",
			mutate: func(m *Manifest) {
				m.MCPServers = map[string]MCPServer{
					"a": {Command: "node", Args: []string{"server.mjs"}},
					"b": {Command: "node", Args: []string{"server.mjs"}},
				}
			},
			want: []wantIssue{{"mcpServers.b", IssueDuplicateSource}},
		},
		{
			name: "two MCP servers with the same url are reported",
			mutate: func(m *Manifest) {
				m.MCPServers = map[string]MCPServer{
					"a": {URL: "https://example.test/mcp"},
					"b": {URL: "https://example.test/mcp"},
				}
			},
			want: []wantIssue{{"mcpServers.b", IssueDuplicateSource}},
		},
		{
			name: "MCP server with neither command nor url is reported",
			mutate: func(m *Manifest) {
				m.MCPServers = map[string]MCPServer{"broken": {Args: []string{"x"}}}
			},
			want: []wantIssue{{"mcpServers.broken", IssueIncompleteMCP}},
		},
		{
			name: "distinct MCP servers and paths produce no issues",
			mutate: func(m *Manifest) {
				m.Skills = []string{"skills"}
				m.MCPServers = map[string]MCPServer{
					"a": {Command: "node", Args: []string{"a.mjs"}},
					"b": {Command: "python", Args: []string{"b.py"}},
				}
			},
		},
		{
			name: "multiple issues are returned in stable field order",
			mutate: func(m *Manifest) {
				m.Name = ""
				m.Version = "bad"
				m.Skills = []string{"../../outside"}
				m.MCPServers = map[string]MCPServer{"broken": {}}
			},
			want: []wantIssue{
				{"name", IssueEmptyField},
				{"version", IssueInvalidVersion},
				{"skills", IssuePathEscape},
				{"mcpServers.broken", IssueIncompleteMCP},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := Manifest{
				Name:    "插件",
				Version: "1.2.3",
				Root:    root,
				Path:    filepath.Join(root, "plugin.json"),
			}
			tc.mutate(&m)

			got := ValidateManifest(m)
			if len(got) != len(tc.want) {
				t.Fatalf("issues = %v, want %v", got, tc.want)
			}
			for i, want := range tc.want {
				if got[i].Field != want.field || got[i].Kind != want.kind {
					t.Errorf("issue %d = {%q %q}, want {%q %q}", i, got[i].Field, got[i].Kind, want.field, want.kind)
				}
				if got[i].Message == "" {
					t.Errorf("issue %d has an empty message", i)
				}
			}
		})
	}
}

// TestValidateManifestCJKNameMessage makes sure a legal CJK plugin name is not
// reported, while an illegal one names the offending value in the message.
func TestValidateManifestCJKNameMessage(t *testing.T) {
	root := t.TempDir()
	base := func(name string) Manifest {
		return Manifest{Name: name, Version: "1.0.0", Root: root, Path: filepath.Join(root, "plugin.json")}
	}
	if issues := ValidateManifest(base("知识库助手")); len(issues) != 0 {
		t.Fatalf("legal CJK name produced issues: %v", issues)
	}
	issues := ValidateManifest(base("助手/子目录"))
	if len(issues) != 1 || issues[0].Kind != IssueInvalidName {
		t.Fatalf("illegal CJK name issues = %v", issues)
	}
	if !strings.Contains(issues[0].Message, "助手/子目录") {
		t.Fatalf("message does not name the offending value: %q", issues[0].Message)
	}
}

// TestValidateManifestIsPure proves the function never stats the filesystem:
// a bundle root that does not exist still yields findings only for structural
// problems, not for missing files (which belong to Manifest.Validate).
func TestValidateManifestIsPure(t *testing.T) {
	missingRoot := filepath.Join(t.TempDir(), "never-created")
	m := Manifest{
		Name:    "ghost",
		Version: "1.0.0",
		Root:    missingRoot,
		Path:    filepath.Join(missingRoot, "plugin.json"),
		Skills:  []string{"skills"},
	}
	if issues := ValidateManifest(m); len(issues) != 0 {
		t.Fatalf("pure validation touched the filesystem: %v", issues)
	}
}
