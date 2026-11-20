package security

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ResolveRealPath returns the true on-disk location of path. It follows
// symlinks on every platform and directory junctions on Windows, where
// filepath.EvalSymlinks deliberately leaves junctions unresolved.
//
// A tail that does not exist yet is re-attached to the longest existing
// ancestor, so a path for a file about to be created is still checked against
// the real location of its parent directory.
//
// The function is deliberately fail-closed: if a path cannot be resolved it
// returns an error instead of falling back to its lexical spelling. Callers
// must treat that error as a denial, not as "probably fine".
func ResolveRealPath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		path = "."
	}
	absolute, err := filepath.Abs(expandHome(strings.TrimSpace(path)))
	if err != nil {
		return "", fmt.Errorf("resolving path %q: %w", path, err)
	}
	return resolveWithTail(filepath.Clean(absolute))
}

// CheckPathInRoots verifies that the real location of path stays inside one
// of roots. Both sides are resolved first: a root reached through a link keeps
// working, while a link or junction inside a root cannot hide an escape.
func CheckPathInRoots(path string, roots []string) error {
	if len(roots) == 0 {
		return fmt.Errorf("security: no roots configured")
	}
	realRoots := make([]string, 0, len(roots))
	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		real, err := ResolveRealPath(root)
		if err != nil {
			return fmt.Errorf("security: resolving root %q: %w", root, err)
		}
		realRoots = append(realRoots, real)
	}
	if len(realRoots) == 0 {
		return fmt.Errorf("security: no roots configured")
	}
	real, err := ResolveRealPath(path)
	if err != nil {
		return fmt.Errorf("security: resolving path %q: %w", path, err)
	}
	if within(real, realRoots) {
		return nil
	}
	return fmt.Errorf("security: real path %q resolves outside the roots %v", path, roots)
}

func resolveWithTail(absolute string) (string, error) {
	if resolved, err := realPathExisting(absolute); err == nil {
		return resolved, nil
	} else if !isMissing(err) {
		return "", err
	}
	tail := ""
	current := absolute
	for {
		// os.Open reports a dangling link as "missing", so without this check
		// the loop would reattach the link's lexical name and hide the fact
		// that it points somewhere else (possibly outside the write roots).
		// Reading the link itself resolves it even before its target exists,
		// which is exactly the write-before-create case boundary checks need.
		if target, ok := linkTarget(current); ok {
			reattached := target
			if tail != "" {
				reattached = filepath.Join(target, tail)
			}
			return resolveWithTail(filepath.Clean(reattached))
		}
		base := filepath.Base(current)
		if tail == "" {
			tail = base
		} else {
			tail = filepath.Join(base, tail)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("security: no resolvable ancestor for %q", absolute)
		}
		current = parent
		if resolved, err := realPathExisting(current); err == nil {
			return filepath.Join(resolved, tail), nil
		} else if !isMissing(err) {
			return "", err
		}
	}
}

// linkTarget returns the resolved target of the symlink or Windows junction at
// path. Unlike realPathExisting it reads the link object itself (Lstat +
// Readlink) instead of opening through it, so it also works for a dangling link
// whose target does not exist yet. ok is false for ordinary paths and when the
// link cannot be read; callers then fall back to the lexical walk.
func linkTarget(path string) (string, bool) {
	info, err := os.Lstat(path)
	if err != nil || !isLinkInfo(info) {
		return "", false
	}
	target, err := os.Readlink(path)
	if err != nil || target == "" {
		return "", false
	}
	target = normalizeLinkTarget(target)
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(path), target)
	}
	return filepath.Clean(target), true
}
