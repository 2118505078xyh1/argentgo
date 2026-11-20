package security

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestIsPathProtectedDirectories(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		name      string
		path      string
		protected bool
		reason    string
	}{
		{"git config", filepath.Join(root, ".git", "config"), true, "protected subpath .git"},
		{"agent state", ".argent/state.json", true, "protected subpath .argent"},
		{"aws credentials", filepath.Join(root, ".aws", "credentials"), true, "protected subpath .aws"},
		{"ssh key", ".ssh/id_rsa", true, "protected subpath .ssh"},
		{"nested git", filepath.Join(root, "vendor", ".git", "config"), true, "protected subpath .git"},
		{"normal source", filepath.Join(root, "src", "main.go"), false, ""},
		{"github is not protected", filepath.Join(root, ".github", "workflows", "ci.yml"), false, ""},
		{"root itself", root, false, ""},
	}
	for _, tc := range cases {
		protected, reason := IsPathProtected(root, tc.path)
		if protected != tc.protected {
			t.Errorf("%s: protected=%v, want %v (reason %q)", tc.name, protected, tc.protected, reason)
		}
		if tc.reason != "" && reason != tc.reason {
			t.Errorf("%s: reason=%q, want %q", tc.name, reason, tc.reason)
		}
		if !protected && reason != "" {
			t.Errorf("%s: unexpected reason %q for a writable path", tc.name, reason)
		}
	}
}

func TestIsPathProtectedFollowsGitdirPointer(t *testing.T) {
	root := t.TempDir()
	external := t.TempDir()
	gitdir := filepath.Join(external, "worktrees", "wt")
	if err := os.MkdirAll(gitdir, 0o755); err != nil {
		t.Fatal(err)
	}
	pointer := "gitdir: " + filepath.ToSlash(gitdir) + "\n"
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte(pointer), 0o644); err != nil {
		t.Fatal(err)
	}

	protected, reason := IsPathProtected(root, filepath.Join(gitdir, "config"))
	if !protected || !strings.Contains(reason, "gitdir") {
		t.Errorf("write under external gitdir: protected=%v, reason=%q", protected, reason)
	}
	if protected, reason := IsPathProtected(root, filepath.Join(root, ".git", "hooks", "pre-commit")); !protected {
		t.Errorf("write to the pointer path itself should stay protected, reason=%q", reason)
	}
	if protected, reason := IsPathProtected(root, filepath.Join(root, "src", "main.go")); protected {
		t.Errorf("normal write became protected: %q", reason)
	}
}

func TestIsPathProtectedResolvesRelativeGitdirPointer(t *testing.T) {
	root := t.TempDir()
	gitdir := filepath.Join(root, ".real-git")
	if err := os.MkdirAll(gitdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: .real-git\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	protected, reason := IsPathProtected(root, filepath.Join(gitdir, "HEAD"))
	if !protected || !strings.Contains(reason, "gitdir") {
		t.Errorf("write under relative gitdir: protected=%v, reason=%q", protected, reason)
	}
}

func TestIsPathProtectedCaseInsensitiveOnWindows(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	protected, reason := IsPathProtected(root, filepath.Join(root, ".GIT", "config"))
	if runtime.GOOS == "windows" {
		if !protected {
			t.Errorf("windows must match .GIT case-insensitively, reason=%q", reason)
		}
	} else if protected {
		t.Errorf("non-windows must match protected names exactly, reason=%q", reason)
	}
}

func TestIsPathProtectedRejectsOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	paths := []string{
		"../outside/file.txt",
		filepath.Join(root, "..", "outside", "file.txt"),
		filepath.Join(outside, "file.txt"),
	}
	for _, path := range paths {
		protected, reason := IsPathProtected(root, path)
		if !protected || !strings.Contains(reason, "outside root") {
			t.Errorf("path %q: protected=%v, reason=%q", path, protected, reason)
		}
	}
}

func TestProtectedSubpathsListsNamesAndGitdir(t *testing.T) {
	root := t.TempDir()
	got := ProtectedSubpaths(root)
	for _, name := range DefaultProtectedNames {
		if !containsPath(got, filepath.Join(root, name)) {
			t.Errorf("ProtectedSubpaths(%q) = %v, missing %s", root, got, name)
		}
	}

	gitdir := filepath.Join(t.TempDir(), "gitdir")
	pointer := "gitdir: " + filepath.ToSlash(gitdir) + "\n"
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte(pointer), 0o644); err != nil {
		t.Fatal(err)
	}
	if !containsPath(ProtectedSubpaths(root), filepath.Clean(gitdir)) {
		t.Errorf("ProtectedSubpaths(%q) should include the resolved gitdir %q", root, gitdir)
	}
}

func TestPolicyEvaluateDeniesProtectedSubpaths(t *testing.T) {
	root := t.TempDir()
	extra := t.TempDir()
	outside := t.TempDir()
	policy := NewPolicy(root, PolicyConfig{WriteRoots: []string{extra}})

	cases := []struct {
		name           string
		tool           string
		path           string
		want           Decision
		reasonContains string
	}{
		{"git config in workspace", "write", filepath.Join(root, ".git", "config"), Deny, "protected subpath .git"},
		{"ssh key in extra root", "write", filepath.Join(extra, ".ssh", "id_rsa"), Deny, "protected subpath .ssh"},
		{"normal write in workspace", "write", filepath.Join(root, "src", "main.go"), Ask, ""},
		{"normal edit in extra root", "edit", filepath.Join(extra, "src", "main.go"), Ask, ""},
		{"outside every write root", "write", filepath.Join(outside, "x.txt"), Deny, "not writable"},
		{"read of git config stays allowed", "read", filepath.Join(root, ".git", "config"), Allow, ""},
	}
	for _, tc := range cases {
		args := []byte(fmt.Sprintf(`{"path":%q}`, tc.path))
		got := policy.Evaluate(tc.tool, args)
		if got.Decision != tc.want {
			t.Errorf("%s: decision=%v, want %v (reason %q)", tc.name, got.Decision, tc.want, got.Reason)
		}
		if tc.reasonContains != "" && !strings.Contains(got.Reason, tc.reasonContains) {
			t.Errorf("%s: reason=%q, want it to contain %q", tc.name, got.Reason, tc.reasonContains)
		}
	}
}

func TestPolicyEvaluateDeniesWorktreeGitdir(t *testing.T) {
	root := t.TempDir()
	gitdir := filepath.Join(root, ".real-git")
	if err := os.MkdirAll(gitdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: .real-git\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	policy := NewPolicy(root, PolicyConfig{})

	args := []byte(fmt.Sprintf(`{"path":%q}`, filepath.Join(gitdir, "hooks", "pre-commit")))
	got := policy.Evaluate("write", args)
	if got.Decision != Deny || !strings.Contains(got.Reason, "gitdir") {
		t.Errorf("worktree gitdir write: %v (%q), want Deny with a gitdir reason", got.Decision, got.Reason)
	}
}

func containsPath(paths []string, want string) bool {
	for _, path := range paths {
		if path == want {
			return true
		}
	}
	return false
}
