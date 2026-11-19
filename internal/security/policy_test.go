package security

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustArgs(t *testing.T, text string) json.RawMessage {
	t.Helper()
	if !json.Valid([]byte(text)) {
		t.Fatalf("invalid test JSON: %s", text)
	}
	return json.RawMessage(text)
}

func TestTraversalIsDenied(t *testing.T) {
	root := t.TempDir()
	policy := DefaultPolicy(root)
	cases := []string{
		"../etc/passwd",
		"..",
		"src/../../etc/passwd",
		"a/b/../../../secrets.txt",
	}
	for _, path := range cases {
		args := mustArgs(t, `{"path":`+quote(path)+`}`)
		result := policy.Evaluate("read", args)
		if result.Decision != Deny {
			t.Fatalf("path %q: got %v, want deny", path, result.Decision)
		}
		if !strings.Contains(result.Reason, "escapes") {
			t.Fatalf("path %q: reason %q does not mention the escape", path, result.Reason)
		}
	}
}

func TestDotSegmentsInsideTheRootAreAllowed(t *testing.T) {
	policy := DefaultPolicy(t.TempDir())
	args := mustArgs(t, `{"path":"./src/./main.go"}`)
	if got := policy.Check("read", args); got != Allow {
		t.Fatalf("got %v, want allow", got)
	}
}

func TestDeniedPathPattern(t *testing.T) {
	policy := NewPolicy(t.TempDir(), PolicyConfig{DenyPaths: []string{"**/.env"}})
	if got := policy.Check("read", mustArgs(t, `{"path":".env"}`)); got != Deny {
		t.Fatalf(".env: got %v, want deny", got)
	}
	if got := policy.Check("read", mustArgs(t, `{"path":"src/main.go"}`)); got != Allow {
		t.Fatalf("src/main.go: got %v, want allow", got)
	}
}

func TestAllowPathsClosesEverythingElse(t *testing.T) {
	root := t.TempDir()
	policy := NewPolicy(root, PolicyConfig{AllowPaths: []string{"src/**"}})
	if got := policy.Check("read", mustArgs(t, `{"path":"src/main.go"}`)); got != Allow {
		t.Fatalf("src/main.go: got %v, want allow", got)
	}
	if got := policy.Check("read", mustArgs(t, `{"path":"docs/readme.md"}`)); got != Deny {
		t.Fatalf("docs/readme.md: got %v, want deny", got)
	}
	if got := policy.Check("write", mustArgs(t, `{"path":"src/../docs/x.md"}`)); got != Deny {
		t.Fatalf("docs via ..: got %v, want deny", got)
	}
}

func TestTildeRuleProtectsTheHomeDirectory(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory")
	}
	policy := NewPolicy(t.TempDir(), PolicyConfig{DenyPaths: []string{"~/.ssh/**"}})
	if got := policy.Check("read", mustArgs(t, `{"path":"~/.ssh/id_rsa"}`)); got != Deny {
		t.Fatalf("tilde argument: got %v, want deny", got)
	}
	absolute := filepath.Join(home, ".ssh", "id_rsa")
	if got := policy.Check("read", mustArgs(t, `{"path":`+quote(absolute)+`}`)); got != Deny {
		t.Fatalf("absolute home path: got %v, want deny", got)
	}
	if got := policy.Check("read", mustArgs(t, `{"path":"src/main.go"}`)); got != Allow {
		t.Fatalf("src/main.go: got %v, want allow", got)
	}
}

func TestPathPrefixRuleBlocksChildren(t *testing.T) {
	policy := NewPolicy(t.TempDir(), PolicyConfig{DenyPaths: []string{"secrets"}})
	if got := policy.Check("read", mustArgs(t, `{"path":"secrets/id_rsa"}`)); got != Deny {
		t.Fatalf("prefix child: got %v, want deny", got)
	}
	if got := policy.Check("read", mustArgs(t, `{"path":"sources/main.go"}`)); got != Allow {
		t.Fatalf("sibling with a similar name: got %v, want allow", got)
	}
}

func TestDeniedCommand(t *testing.T) {
	policy := NewPolicy(t.TempDir(), PolicyConfig{DenyCommands: []string{"git push --force"}})
	if got := policy.Check("bash", mustArgs(t, `{"command":"git push --force origin main"}`)); got != Deny {
		t.Fatalf("force push: got %v, want deny", got)
	}
	if got := policy.Check("bash", mustArgs(t, `{"command":"git status"}`)); got != Ask {
		t.Fatalf("git status: got %v, want ask", got)
	}
}

func TestDeniedCommandPath(t *testing.T) {
	policy := NewPolicy(t.TempDir(), PolicyConfig{DenyCommandPaths: []string{"~/.ssh"}})
	if got := policy.Check("bash", mustArgs(t, `{"command":"cat ~/.ssh/id_rsa"}`)); got != Deny {
		t.Fatalf("ssh read: got %v, want deny", got)
	}
	if got := policy.Check("bash", mustArgs(t, `{"command":"cat README.md"}`)); got != Ask {
		t.Fatalf("readme: got %v, want ask", got)
	}
}

func TestDeniedToolAndAllowList(t *testing.T) {
	denied := NewPolicy(t.TempDir(), PolicyConfig{DenyTools: []string{"bash"}})
	if got := denied.Check("bash", mustArgs(t, `{"command":"ls"}`)); got != Deny {
		t.Fatalf("denied tool: got %v, want deny", got)
	}
	if got := denied.Check("read", mustArgs(t, `{"path":"a.txt"}`)); got != Allow {
		t.Fatalf("read next to a denied tool: got %v, want allow", got)
	}

	whitelist := NewPolicy(t.TempDir(), PolicyConfig{AllowTools: []string{"read"}})
	if got := whitelist.Check("ls", mustArgs(t, `{"path":"."}`)); got != Deny {
		t.Fatalf("ls outside the whitelist: got %v, want deny", got)
	}
	if got := whitelist.Check("read", mustArgs(t, `{"path":"a.txt"}`)); got != Allow {
		t.Fatalf("read inside the whitelist: got %v, want allow", got)
	}
}

func TestUnknownToolAsksAndKnownToolsDoNot(t *testing.T) {
	policy := DefaultPolicy(t.TempDir())
	if got := policy.Check("deploy", mustArgs(t, `{}`)); got != Ask {
		t.Fatalf("unknown tool: got %v, want ask", got)
	}
	if got := policy.Check("read", mustArgs(t, `{"path":"a.txt"}`)); got != Allow {
		t.Fatalf("read: got %v, want allow", got)
	}
	if got := policy.Check("write", mustArgs(t, `{"path":"a.txt"}`)); got != Ask {
		t.Fatalf("write: got %v, want ask", got)
	}
}

func TestUnparsableArgumentsAsk(t *testing.T) {
	policy := DefaultPolicy(t.TempDir())
	result := policy.Evaluate("read", json.RawMessage(`not json`))
	if result.Decision != Ask {
		t.Fatalf("got %v, want ask", result.Decision)
	}
	if result.Reason == "" {
		t.Fatal("an ask must carry a reason")
	}
}

func TestOrderedRuleTable(t *testing.T) {
	policy := NewPolicy(t.TempDir(), PolicyConfig{Rules: []Rule{
		{Decision: Deny, Tool: "read", Path: "**/.env"},
		{Decision: Allow, Tool: "read", Path: "src/**"},
		{Decision: Ask, Tool: "bash", Command: "git push*"},
		{Decision: Deny, Tool: "bash", Command: "rm -rf*"},
	}})
	cases := []struct {
		name string
		args string
		want Decision
	}{
		{"read", `{"path":".env"}`, Deny},
		{"read", `{"path":"src/main.rs"}`, Allow},
		{"bash", `{"command":"git push origin main"}`, Ask},
		{"bash", `{"command":"rm -rf /"}`, Deny},
	}
	for _, tc := range cases {
		if got := policy.Check(tc.name, mustArgs(t, tc.args)); got != tc.want {
			t.Fatalf("%s %s: got %v, want %v", tc.name, tc.args, got, tc.want)
		}
	}
}

func TestPolicyFromJSON(t *testing.T) {
	policy, err := PolicyFromJSON(t.TempDir(), []byte(`{
		"denyPaths": ["**/.env", "~/.ssh/**"],
		"denyCommands": "git push --force, rm -rf /",
		"denyTools": ["bash"],
		"allowTools": ["read", "grep"],
		"rules": [{"decision":"ask","tool":"write","path":"src/**"}]
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if policy.IsEmpty() {
		t.Fatal("a populated policy must not be empty")
	}
	if got := policy.Check("bash", mustArgs(t, `{"command":"ls"}`)); got != Deny {
		t.Fatalf("denied tool: got %v, want deny", got)
	}
	if got := policy.Check("write", mustArgs(t, `{"path":"src/a.go"}`)); got != Deny {
		t.Fatalf("write outside the allow list: got %v, want deny", got)
	}
	if got := policy.Check("read", mustArgs(t, `{"path":"src/a.go"}`)); got != Allow {
		t.Fatalf("read: got %v, want allow", got)
	}
	if _, err := PolicyFromJSON(t.TempDir(), []byte(`{`)); err == nil {
		t.Fatal("malformed JSON must fail")
	}
	if !DefaultPolicy(t.TempDir()).IsEmpty() {
		t.Fatal("a default policy must be empty")
	}
}

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pattern string
		path    string
		want    bool
	}{
		{"*.env", "project/src/.env", true},
		{"src/**", "src/a/b.rs", true},
		{"**/*.pem", "home/user/.ssh/key.pem", true},
		{"project/src/*", "project/src/main.rs", true},
		{"src/**", "docs/readme.md", false},
		{"*.env", "project/src/main.rs", false},
		{"src", "a/src/b/c.rs", true},
		{"src", "sources/main.go", false},
		{"file?.go", "dir/file1.go", true},
	}
	for _, tc := range cases {
		if got := GlobMatch(tc.pattern, tc.path); got != tc.want {
			t.Fatalf("GlobMatch(%q, %q) = %v, want %v", tc.pattern, tc.path, got, tc.want)
		}
	}
}

func quote(text string) string {
	encoded, _ := json.Marshal(text)
	return string(encoded)
}
