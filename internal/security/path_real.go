package security

import (
	"fmt"
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
