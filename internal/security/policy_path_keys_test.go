package security

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestDenyPathsApplyToEveryPathArgumentName guards the fix for a real policy
// bypass: pathValues used to recognise only "path"/"file"/"dir"/"directory"/
// "target", while the built-in diff tool spells its operands "path_a" and
// "path_b". A deny-path rule therefore saw no path at all in a diff call and
// let a denied file be read (Ask falls through to auto-approval).
func TestDenyPathsApplyToEveryPathArgumentName(t *testing.T) {
	root := t.TempDir()
	policy := NewPolicy(root, PolicyConfig{DenyPaths: []string{"**/.env"}})

	for _, key := range pathArgKeys {
		t.Run(key, func(t *testing.T) {
			args := json.RawMessage(fmt.Sprintf(`{%q:".env"}`, key))
			got := policy.Evaluate("read", args)
			if got.Decision != Deny {
				t.Fatalf("read with %s=.env: decision = %v (%q), want Deny", key, got.Decision, got.Reason)
			}
		})
	}

	// A field whose name does not denote a path must stay outside the path
	// rules, otherwise the fix would turn into an over-broad string scan.
	t.Run("non path field untouched", func(t *testing.T) {
		args := json.RawMessage(`{"note":".env"}`)
		if got := policy.Evaluate("read", args); got.Decision != Allow {
			t.Fatalf("read with a non-path field: decision = %v (%q), want Allow", got.Decision, got.Reason)
		}
	})
}

// TestDenyPathsApplyToEveryPathArray covers the array forms: "paths" from the
// checkpoint restore manifest and "files" from context_pack. Both read file
// contents, so a deny-path rule must apply to their elements too.
func TestDenyPathsApplyToEveryPathArray(t *testing.T) {
	root := t.TempDir()
	policy := NewPolicy(root, PolicyConfig{DenyPaths: []string{"**/.env"}})
	for _, key := range pathArrayKeys {
		t.Run(key, func(t *testing.T) {
			args := json.RawMessage(fmt.Sprintf(`{%q:["main.go",".env"]}`, key))
			got := policy.Evaluate("read", args)
			if got.Decision != Deny {
				t.Fatalf("read with %s containing .env: decision = %v (%q), want Deny", key, got.Decision, got.Reason)
			}
		})
	}
}

// TestRuleDecisionCoversAlternatePathKeys proves the permissions table sees
// the same resources as the deny-path gate, including array entries under the
// new key names. A deny row anywhere in the table is absolute.
func TestRuleDecisionCoversAlternatePathKeys(t *testing.T) {
	root := t.TempDir()
	policy := NewPolicy(root, PolicyConfig{Rules: []Rule{
		{Decision: Ask, Tool: "read", Path: "**/notes.txt"},
		{Decision: Deny, Tool: "read", Path: "**/secret.txt"},
	}})

	cases := []struct {
		name string
		args string
		want Decision
	}{
		{"scalar path_a", `{"path_a":"notes.txt","path_b":"main.go"}`, Ask},
		{"scalar file_path", `{"file_path":"notes.txt"}`, Ask},
		{"array files", `{"target":"main.go","files":["notes.txt"]}`, Ask},
		{"array paths", `{"paths":["main.go","notes.txt"]}`, Ask},
		// A deny on an array entry must win over an Ask on a scalar: this is
		// the strictest-wins walk that a missing key used to break.
		{"deny in path_b", `{"path_a":"notes.txt","path_b":"secret.txt"}`, Deny},
		{"deny in files", `{"files":["notes.txt","secret.txt"]}`, Deny},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := policy.Evaluate("read", json.RawMessage(tc.args))
			if got.Decision != tc.want {
				t.Fatalf("Evaluate(%s) = %v (%q), want %v", tc.args, got.Decision, got.Reason, tc.want)
			}
		})
	}
}

// TestWriteRootsCoverAlternatePathKeys verifies that a mutating tool cannot
// escape the write boundary just by naming its path argument differently.
func TestWriteRootsCoverAlternatePathKeys(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "escape.txt")
	policy := NewPolicy(root, PolicyConfig{})

	for _, key := range []string{"path_a", "file_path", "old_path"} {
		t.Run(key, func(t *testing.T) {
			args := json.RawMessage(fmt.Sprintf(`{%q:%q}`, key, filepath.ToSlash(target)))
			got := policy.Evaluate("write", args)
			if got.Decision != Deny {
				t.Fatalf("write with %s outside the root: decision = %v (%q), want Deny", key, got.Decision, got.Reason)
			}
		})
	}
}

// TestApprovalArgsRewritesAlternatePathKeys keeps the prompt display honest:
// a link under an alternate path key must be shown at its real location, not
// at the harmless lexical spelling the tool received.
func TestApprovalArgsRewritesAlternatePathKeys(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "note.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	makeDirectoryLink(t, outside, alias)

	want, err := ResolveRealPath(filepath.Join(root, "alias", "note.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"path_a", "file_path"} {
		t.Run(key, func(t *testing.T) {
			args := json.RawMessage(fmt.Sprintf(`{%q:"alias/note.txt"}`, key))
			var shown map[string]string
			if err := json.Unmarshal(approvalArgs(args, root), &shown); err != nil {
				t.Fatal(err)
			}
			if shown[key] != want {
				t.Fatalf("approvalArgs(%s)[%s] = %q, want the real path %q", args, key, shown[key], want)
			}
		})
	}
}
