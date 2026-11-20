package security

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// DefaultProtectedNames are subpath names that stay read-only even inside a
// writable root. They hold git hooks/object metadata (.git), agent state
// (.argent), cloud credentials (.aws) and private keys (.ssh): a write to one
// of them turns an ordinary workspace write into a persistence or privilege
// change. Matching is by whole path segment, so a file in .github is untouched.
var DefaultProtectedNames = []string{".git", ".argent", ".aws", ".ssh"}

// maxGitPointerBytes bounds the best-effort read of a .git pointer file so a
// corrupt or hostile file cannot make policy evaluation expensive.
const maxGitPointerBytes = 4096

// IsPathProtected reports whether path falls under a protected subpath of
// root. path may be absolute or relative to root; the returned reason is a
// short, operator-facing explanation and is empty only for a writable path.
//
// A .git *file* is followed as well: git uses one for worktrees and submodules,
// and its gitdir: target is where the hooks live. That target often sits
// outside root, so the pointer check deliberately runs before the escape
// check, giving a more specific reason for writes aimed at it.
//
// Both the lexical path and its real on-disk location are checked, so a
// symlink or Windows junction inside the root cannot disguise a write into a
// protected subpath as an ordinary workspace write.
func IsPathProtected(root, path string) (bool, string) {
	rootAbs := resolveAgainst(root, "")
	pathAbs := resolveAgainst(path, rootAbs)

	if protected, reason := protectedWithin(rootAbs, pathAbs); protected {
		return true, reason
	}
	// A link *inside* the root can point at a protected subpath while its own
	// lexical spelling is harmless (e.g. <root>/alias -> <root>/.ssh). The
	// lexical pass above cannot see that, so resolve both sides and repeat the
	// check on the real on-disk location. Without this, one junction is enough
	// to turn a write into .git/.ssh into an ordinary workspace write. A
	// resolution failure is left to the fail-closed boundary checks in
	// InWriteRoots and Policy.checkPath; it cannot widen protection here.
	realRoot, rootErr := ResolveRealPath(rootAbs)
	realPath, pathErr := ResolveRealPath(pathAbs)
	if rootErr == nil && pathErr == nil {
		if protected, reason := protectedWithin(realRoot, realPath); protected {
			return true, reason
		}
	}
	return false, ""
}

// protectedWithin is the whole-segment check for one (root, path) pair. The
// .git pointer refinement runs first so a worktree gitdir gets a specific
// reason even though it sits outside root; outside-root paths stay protected
// too. It is the shared body of the lexical and real-location passes above.
func protectedWithin(rootAbs, pathAbs string) (bool, string) {
	if gitdir, ok := gitdirPointer(rootAbs); ok && pathWithin(gitdir, pathAbs) {
		return true, "protected subpath .git (worktree gitdir)"
	}
	rel, ok := relativeSegments(rootAbs, pathAbs)
	if !ok {
		return true, "outside root"
	}
	for _, segment := range rel {
		for _, name := range DefaultProtectedNames {
			if segmentsEqual(segment, name) {
				return true, "protected subpath " + name
			}
		}
	}
	return false, ""
}

// ProtectedSubpaths lists the concrete absolute paths protected directly
// under root: one per protected name, plus the resolved gitdir when root/.git
// is a pointer file. It is for tests and introspection; the authoritative
// check is IsPathProtected, which also catches nested protected segments.
func ProtectedSubpaths(root string) []string {
	rootAbs := resolveAgainst(root, "")
	out := make([]string, 0, len(DefaultProtectedNames)+1)
	for _, name := range DefaultProtectedNames {
		out = append(out, filepath.Join(rootAbs, name))
	}
	if gitdir, ok := gitdirPointer(rootAbs); ok {
		out = append(out, gitdir)
	}
	return out
}

// protectedInWriteRoots is the bridge used by Policy.checkWriteRoots. The path
// is checked against the root that actually contains it, so a path in an extra
// write root is not misreported as "outside" the workspace root.
func protectedInWriteRoots(path string, roots []string) (bool, string) {
	base := ""
	if len(roots) > 0 && !filepath.IsAbs(expandHome(strings.TrimSpace(path))) {
		base = roots[0]
	}
	candidate := resolveAgainst(path, base)
	for _, root := range roots {
		rootAbs := resolveAgainst(root, "")
		if pathWithin(rootAbs, candidate) {
			return IsPathProtected(rootAbs, candidate)
		}
	}
	return false, ""
}

// resolveAgainst makes path absolute and clean. A relative path is joined to
// base when one is given, otherwise to the current directory.
func resolveAgainst(path, base string) string {
	candidate := expandHome(strings.TrimSpace(path))
	if candidate == "" {
		candidate = "."
	}
	if !filepath.IsAbs(candidate) && base != "" {
		candidate = filepath.Join(base, candidate)
	}
	if absolute, err := filepath.Abs(candidate); err == nil {
		candidate = absolute
	}
	return filepath.Clean(candidate)
}

// gitdirPointer reads <root>/.git when it is a small regular file and returns
// the absolute git directory declared by its "gitdir:" line. Errors are
// ignored: this is a best-effort refinement of a boundary check that fails
// closed elsewhere. The size cap keeps the read bounded.
func gitdirPointer(root string) (string, bool) {
	dotGit := filepath.Join(root, ".git")
	info, err := os.Stat(dotGit)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxGitPointerBytes {
		return "", false
	}
	data, err := os.ReadFile(dotGit)
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(strings.ToLower(line), "gitdir:") {
			continue
		}
		target := strings.TrimSpace(line[len("gitdir:"):])
		if target == "" {
			return "", false
		}
		return resolveAgainst(target, root), true
	}
	return "", false
}

// pathWithin reports whether candidate is base or lies beneath it.
func pathWithin(base, candidate string) bool {
	_, ok := relativeSegments(base, candidate)
	return ok
}

// relativeSegments returns candidate's segments below base. ok is false when
// candidate is outside base, which includes a different Windows volume.
func relativeSegments(base, candidate string) ([]string, bool) {
	baseSegs := pathSegments(base)
	candSegs := pathSegments(candidate)
	if len(candSegs) < len(baseSegs) {
		return nil, false
	}
	for i, segment := range baseSegs {
		if !segmentsEqual(segment, candSegs[i]) {
			return nil, false
		}
	}
	return candSegs[len(baseSegs):], true
}

// pathSegments splits a cleaned path into comparable segments, dropping the
// root marker and "." entries.
func pathSegments(path string) []string {
	parts := strings.Split(filepath.Clean(path), string(filepath.Separator))
	segments := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" && part != "." {
			segments = append(segments, part)
		}
	}
	return segments
}

// segmentsEqual is case-insensitive on Windows (where the filesystem is) and
// exact elsewhere, so a Linux .GIT directory is a different name.
func segmentsEqual(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
