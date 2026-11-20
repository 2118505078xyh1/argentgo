package memory

// Hierarchical, advisory .argentignore discovery.
//
// Unlike the instruction files in memory.go (which are injected into the
// prompt), these files only say what a caller may choose to leave OUT of the
// prompt. This mirrors Cursor's .cursorignore: it is a best-effort convenience
// for keeping obviously irrelevant paths out of context, and is explicitly
// NOT a security boundary. Never use IgnoreRules.Ignored to decide whether
// untrusted content may be read or executed — use internal/security for that.

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

// IgnoreFileName is the per-directory exclusion file discovered by LoadIgnore.
const IgnoreFileName = ".argentignore"

// maxIgnoreFileBytes caps how much of one .argentignore file is read. Ignore
// files are advisory, so an oversized file is skipped rather than failing the
// whole walk.
const maxIgnoreFileBytes = 1 << 20

// pattern is one parsed .argentignore line.
//
// Matching reuses security.GlobMatch: memory already depends on
// internal/security (see bank.go), so there is no import cycle, and reusing
// it keeps glob semantics identical to the policy matcher (`*` inside a
// segment, `**` across segments, `?` for one character, plus tail matching).
// Only gitignore-style line handling (`#`, `!`, blank lines) is added here.
type pattern struct {
	glob    string
	negated bool
	source  string // file the line came from, kept for diagnostics
}

// IgnoreRules is the flattened set of .argentignore patterns discovered by
// LoadIgnore, ordered parent directory first so the last matching pattern
// (the most specific one) wins.
//
// Best-effort and advisory, like Cursor's .cursorignore: NOT a security
// boundary. A false negative only means extra text reaches the context.
type IgnoreRules struct {
	patterns []pattern
}

// LoadIgnore walks up from dir collecting .argentignore files. Discovery goes
// child -> parent, but the rules are stored parent -> child so a nested
// directory can override an ancestor with `!`.
//
// Each line is one glob; blank lines and lines starting with `#` are ignored,
// and a leading `!` re-includes a path an earlier pattern excluded. A trailing
// `/` is dropped so directory patterns such as `build/` also match their
// contents. Paths are normalized with filepath.ToSlash before matching.
//
// Only an invalid dir (stat/absolute-path failure) is an error. Unreadable,
// oversized or misnamed entries are skipped: discovery is best-effort and
// must never break prompt construction.
func LoadIgnore(dir string) (*IgnoreRules, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("memory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("memory: %s is not a directory", abs)
	}

	// Walk child -> parent, then reverse to get parent -> child precedence.
	// maxDepth is the same guard used by memory.go for upward walks.
	var found []string
	current := filepath.Clean(abs)
	for depth := 0; depth < maxDepth; depth++ {
		path := filepath.Join(current, IgnoreFileName)
		if st, statErr := os.Stat(path); statErr == nil && !st.IsDir() {
			found = append(found, path)
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}

	rules := &IgnoreRules{}
	for i := len(found) - 1; i >= 0; i-- {
		f, err := os.Open(found[i])
		if err != nil {
			continue
		}
		raw, err := io.ReadAll(io.LimitReader(f, maxIgnoreFileBytes+1))
		_ = f.Close()
		if err != nil || len(raw) > maxIgnoreFileBytes {
			continue
		}
		rules.patterns = append(rules.patterns, parseIgnorePatterns(string(raw), found[i])...)
	}
	return rules, nil
}

// parseIgnorePatterns turns one file's text into ordered patterns.
func parseIgnorePatterns(text, source string) []pattern {
	var out []pattern
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		negated := false
		if strings.HasPrefix(line, "!") {
			negated = true
			line = strings.TrimSpace(line[1:])
		}
		line = strings.Trim(line, "/")
		if line == "" {
			continue
		}
		out = append(out, pattern{
			glob:    filepath.ToSlash(line),
			negated: negated,
			source:  source,
		})
	}
	return out
}

// Ignored reports whether relPath is excluded by the last matching pattern.
// relPath is interpreted relative to the directory passed to LoadIgnore; both
// separators are accepted because filepath.ToSlash normalizes Windows paths.
// A nil ruleset, or one with no patterns, ignores nothing.
//
// Matching is case-insensitive on every platform, inheriting GlobMatch's
// lowercasing (the same behaviour security policy uses), so the result does
// not change between Windows and Linux.
func (r *IgnoreRules) Ignored(relPath string) bool {
	if r == nil || len(r.patterns) == 0 {
		return false
	}
	rel := filepath.ToSlash(filepath.Clean(relPath))
	rel = strings.TrimPrefix(rel, "./")
	rel = strings.TrimPrefix(rel, "/")
	if rel == "." || rel == "" {
		return false
	}

	ignored := false
	for _, p := range r.patterns {
		if security.GlobMatch(p.glob, rel) {
			ignored = !p.negated
		}
	}
	return ignored
}
