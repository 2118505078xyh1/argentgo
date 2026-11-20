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
// contents. Patterns are normalized to forward-slash globs before matching.
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

// normalizeIgnoreGlob converts a trimmed .argentignore line into the canonical
// glob form shared by the loader and the diagnostics. .argentignore is
// gitignore-style, where `/` is the separator on every platform, so backslashes
// are normalized unconditionally: filepath.ToSlash only converts the *native*
// separator and would leave `\` intact on Linux (where it is a legal filename
// byte). security.GlobMatch likewise turns `\` into `/` on both sides, so this
// keeps the stored glob identical to what matching actually uses everywhere.
func normalizeIgnoreGlob(line string) string {
	return strings.ReplaceAll(filepath.ToSlash(line), `\`, "/")
}

// parseIgnorePatterns turns one file's text into ordered patterns.
func parseIgnorePatterns(text, source string) []pattern {
	var out []pattern
	// A leading UTF-8 BOM is encoding metadata, not part of the first line.
	// The instruction-file loader strips it in readInstructionFile; ignore
	// files must do the same, or the first pattern is prefixed with the BOM
	// (and never matches) while a BOM-prefixed comment is read as a pattern.
	text = strings.TrimPrefix(text, bom)
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
			glob:    normalizeIgnoreGlob(line),
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

// IgnoreRuleInfo is one parsed ignore pattern together with a human-readable
// explanation, produced by DescribeIgnoreRules. It is a pure diagnostic value:
// no disk I/O is performed and nothing is injected into the prompt.
type IgnoreRuleInfo struct {
	// Pattern is the original line as supplied to DescribeIgnoreRules.
	Pattern string
	// Glob is the normalized pattern actually used for matching: the leading
	// `!` removed, surrounding `/` trimmed and separators made forward slashes,
	// exactly as parseIgnorePatterns produces.
	Glob string
	// Negated reports whether the line was a `!` re-include rather than an
	// exclusion.
	Negated bool
	// Description is a short semantic explanation of what the pattern does
	// (literal path vs. glob metacharacters, exclusion vs. re-include).
	Description string
	// SensitiveRisk reports whether an exclusion pattern may accidentally also
	// match a sensitive path (a secret or agent metadata location). Negated
	// patterns re-include and therefore never carry this risk.
	SensitiveRisk bool
	// RiskReason lists the representative sensitive probe paths the pattern
	// matched; it is empty when SensitiveRisk is false.
	RiskReason string
}

// sensitiveProbes are representative paths used only for diagnostics. A rule
// that matches any of them is flagged as a possible accidental exclusion. The
// set mixes common secret/metadata locations with CJK names so the check also
// exercises non-ASCII paths. This is a best-effort warning, NOT a security
// boundary: it is intentionally small and explainable rather than exhaustive.
var sensitiveProbes = []string{
	".ssh",
	".ssh/id_rsa",
	".aws/credentials",
	".env",
	".git/config",
	".argent",
	"密钥/私钥.pem",
	"凭证.txt",
}

// DescribeIgnoreRules parses raw .argentignore-style pattern lines and returns
// one IgnoreRuleInfo per effective pattern, in input order. It is the pure
// diagnostic companion to parseIgnorePatterns: line handling (BOM, comments,
// blank lines, bare `!`, leading `/`, separator normalization) is identical, so the reported
// Glob is exactly the string Ignored would match against.
//
// Blank lines, comments and bare `!` lines are skipped, matching the loader;
// an empty (or all-skipped) input returns nil. The function never reads files
// and never mutates its input.
func DescribeIgnoreRules(patterns []string) []IgnoreRuleInfo {
	var out []IgnoreRuleInfo
	for _, raw := range patterns {
		// Strip a leading BOM the same way the file loader does; otherwise the
		// first "pattern" would be BOM-prefixed and a BOM-prefixed comment would
		// be mistaken for a real rule.
		line := strings.TrimPrefix(strings.TrimSpace(raw), bom)
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "!") && strings.TrimSpace(line[1:]) == "" {
			// A bare `!` is discarded by the loader rather than becoming a
			// match-all re-include; mirror that here.
			continue
		}
		info := describePattern(raw, line)
		if info.Glob == "" {
			continue
		}
		out = append(out, info)
	}
	return out
}

// describePattern builds one IgnoreRuleInfo. line is the already BOM-trimmed,
// whitespace-trimmed input; raw is kept verbatim for the Pattern field.
func describePattern(raw, line string) IgnoreRuleInfo {
	info := IgnoreRuleInfo{Pattern: raw}
	negated := strings.HasPrefix(line, "!")
	if negated {
		line = strings.TrimSpace(line[1:])
	}
	glob := normalizeIgnoreGlob(strings.Trim(line, "/"))
	info.Glob = glob
	info.Negated = negated

	action := "exclude"
	if negated {
		action = "re-include"
	}
	syntax := describeGlobSyntax(glob)
	if !strings.ContainsAny(glob, "*?[") {
		info.Description = fmt.Sprintf("%s the literal path %q (%s)", action, glob, syntax)
	} else {
		info.Description = fmt.Sprintf("%s paths matching glob %q [%s]", action, glob, syntax)
	}

	// Only exclusions can "harm" a path by leaving it out of the context.
	// Match against the probe paths with the very same matcher Ignored uses,
	// so a flagged pattern is guaranteed to actually match that path.
	if !negated {
		var matched []string
		for _, probe := range sensitiveProbes {
			if security.GlobMatch(glob, probe) {
				matched = append(matched, probe)
			}
		}
		info.SensitiveRisk = len(matched) > 0
		info.RiskReason = strings.Join(matched, ", ")
	}
	return info
}

// describeGlobSyntax explains which glob metacharacters a pattern uses.
func describeGlobSyntax(glob string) string {
	switch {
	case strings.Contains(glob, "**"):
		return "`**` spans directories"
	case strings.Contains(glob, "*"):
		return "`*` matches within one segment"
	case strings.Contains(glob, "?"):
		return "`?` matches one character"
	default:
		return "matches it or anything below it"
	}
}
