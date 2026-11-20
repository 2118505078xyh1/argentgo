package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeIgnoreFile(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, IgnoreFileName)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func loadIgnoreOrFatal(t *testing.T, dir string) *IgnoreRules {
	t.Helper()
	rules, err := LoadIgnore(dir)
	if err != nil {
		t.Fatalf("LoadIgnore(%q): %v", dir, err)
	}
	return rules
}

// No rules means nothing is excluded, including for a nil ruleset.
func TestLoadIgnoreNoRulesIgnoresNothing(t *testing.T) {
	dir := t.TempDir()
	rules := loadIgnoreOrFatal(t, dir)
	if len(rules.patterns) != 0 {
		t.Fatalf("unexpected patterns: %+v", rules.patterns)
	}
	for _, path := range []string{"a.go", "src/a.go", ""} {
		if rules.Ignored(path) {
			t.Errorf("Ignored(%q) = true with no rules", path)
		}
	}
	var nilRules *IgnoreRules
	if nilRules.Ignored("anything") {
		t.Error("nil ruleset ignored a path")
	}
}

// A parent's rule applies below it; a child's later rule wins, and `!`
// re-includes what an earlier pattern excluded.
func TestLoadIgnoreParentChildPrecedenceAndNegation(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "pkg")
	if err := os.MkdirAll(child, 0o700); err != nil {
		t.Fatal(err)
	}
	writeIgnoreFile(t, root, "*.log\nbuild/\n")
	writeIgnoreFile(t, child, "!keep.log\n*.tmp\n")

	rules := loadIgnoreOrFatal(t, child)
	if got := len(rules.patterns); got != 4 {
		t.Fatalf("got %d patterns, want 4: %+v", got, rules.patterns)
	}
	// Parent rules were collected first, so child rules come last.
	if !strings.Contains(rules.patterns[0].glob, "log") || !rules.patterns[2].negated {
		t.Fatalf("unexpected pattern order: %+v", rules.patterns)
	}

	cases := []struct {
		path string
		want bool
	}{
		{"debug.log", true},    // parent rule
		{"keep.log", false},    // child `!` overrides parent
		{"build/out.go", true}, // parent directory rule
		{"cache.tmp", true},    // child rule
		{"main.go", false},
	}
	for _, tc := range cases {
		if got := rules.Ignored(tc.path); got != tc.want {
			t.Errorf("Ignored(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// A `!` re-includes what an earlier pattern excluded; a final plain pattern
// excludes it again. The last matching line always decides.
func TestLoadIgnoreLastMatchingPatternWins(t *testing.T) {
	dir := t.TempDir()
	writeIgnoreFile(t, dir, "secret*\n!secret-public.txt\nsecret-public.txt\n")

	rules := loadIgnoreOrFatal(t, dir)
	if !rules.Ignored("secret-key.txt") {
		t.Error("first pattern should ignore secret-key.txt")
	}
	if !rules.Ignored("secret-public.txt") {
		t.Error("last pattern should re-exclude secret-public.txt")
	}
	if rules.Ignored("public.txt") {
		t.Error("unmatched path should not be ignored")
	}
}

// Comments, blank lines and whitespace-only lines are not patterns; a lone
// `!` is discarded rather than becoming a match-all re-include.
func TestLoadIgnoreSkipsCommentsBlanksAndBareBang(t *testing.T) {
	dir := t.TempDir()
	writeIgnoreFile(t, dir, "# a comment\n\n   \n!   \nkeep.go\n")

	rules := loadIgnoreOrFatal(t, dir)
	if len(rules.patterns) != 1 {
		t.Fatalf("got %d patterns, want 1: %+v", len(rules.patterns), rules.patterns)
	}
	if !rules.Ignored("keep.go") || rules.Ignored("other.go") {
		t.Error("only keep.go should be ignored")
	}
}

// Glob semantics come from security.GlobMatch: `*`, `**` and `?`.
func TestLoadIgnoreGlobSemantics(t *testing.T) {
	dir := t.TempDir()
	writeIgnoreFile(t, dir, "*.tmp\nsrc/**/generated.go\ntest?.go\n")

	rules := loadIgnoreOrFatal(t, dir)
	cases := []struct {
		path string
		want bool
	}{
		{"notes.tmp", true},               // `*` matches in a tail
		{"a/b/notes.tmp", true},           // pattern without slash matches any tail
		{"src/parser/generated.go", true}, // `**` crosses segments
		{"src/generated.go", true},        // `**` also matches zero segments
		{"lib/generated.go", false},
		{"test1.go", true}, // `?` matches one character
		{"testx.go", true},
		{"test12.go", false}, // not two characters
		{"src/parser/handwritten.go", false},
	}
	for _, tc := range cases {
		if got := rules.Ignored(tc.path); got != tc.want {
			t.Errorf("Ignored(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// Paths are normalized with filepath.ToSlash; native separators, leading `./`
// and leading `/` all resolve to the same path.
func TestLoadIgnorePathNormalization(t *testing.T) {
	dir := t.TempDir()
	writeIgnoreFile(t, dir, "secret.txt\n")

	rules := loadIgnoreOrFatal(t, dir)
	native := filepath.Join("a", "b", "secret.txt")
	for _, path := range []string{"secret.txt", native, "./secret.txt", "/secret.txt"} {
		if !rules.Ignored(path) {
			t.Errorf("Ignored(%q) = false, want true", path)
		}
	}
	if rules.Ignored(filepath.Join("a", "b", "other.txt")) {
		t.Error("unrelated path should not be ignored")
	}
}

// GlobMatch lowercases both sides, so ignore matching is case-insensitive on
// every platform (the same rule the security policy uses).
func TestLoadIgnoreCaseInsensitiveMatching(t *testing.T) {
	dir := t.TempDir()
	writeIgnoreFile(t, dir, "Secret.TXT\nBuild/\n")

	rules := loadIgnoreOrFatal(t, dir)
	for _, path := range []string{"secret.txt", "SECRET.txt", "build/output.o", "BUILD/output.o"} {
		if !rules.Ignored(path) {
			t.Errorf("Ignored(%q) = false, want true (case-insensitive)", path)
		}
	}
}

// Invalid input is an error; an entry named .argentignore that is a directory
// is simply skipped, keeping discovery best-effort.
func TestLoadIgnoreInvalidDirAndDirectoryEntry(t *testing.T) {
	if _, err := LoadIgnore(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("LoadIgnore on a missing directory should fail")
	}
	file := filepath.Join(t.TempDir(), "not-a-dir.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadIgnore(file); err == nil {
		t.Error("LoadIgnore on a file should fail")
	}

	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, IgnoreFileName), 0o700); err != nil {
		t.Fatal(err)
	}
	rules := loadIgnoreOrFatal(t, dir)
	if rules.Ignored("anything") {
		t.Error("directory named .argentignore should contribute no rules")
	}
}
