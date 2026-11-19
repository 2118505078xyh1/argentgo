package security

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SandboxMode is the file-tool policy selected by --sandbox-mode.
type SandboxMode string

const (
	// SandboxReadOnly drops every mutating tool.
	SandboxReadOnly SandboxMode = "read-only"
	// SandboxWorkspaceWrite allows writes, restricted to the write roots.
	SandboxWorkspaceWrite SandboxMode = "workspace-write"
	// SandboxDangerFullAccess removes the write-root boundary.
	SandboxDangerFullAccess SandboxMode = "danger-full-access"
)

// FromString parses a --sandbox-mode value. An empty string selects the
// default, workspace-write.
func FromString(value string) (SandboxMode, error) {
	switch SandboxMode(strings.ToLower(strings.TrimSpace(value))) {
	case "":
		return SandboxWorkspaceWrite, nil
	case SandboxReadOnly:
		return SandboxReadOnly, nil
	case SandboxWorkspaceWrite:
		return SandboxWorkspaceWrite, nil
	case SandboxDangerFullAccess:
		return SandboxDangerFullAccess, nil
	}
	return "", fmt.Errorf("security: unknown sandbox mode %q (want read-only | workspace-write | danger-full-access)", value)
}

// String returns the CLI spelling.
func (m SandboxMode) String() string { return string(m) }

// Valid reports whether m is one of the three accepted modes.
func (m SandboxMode) Valid() bool {
	switch m {
	case SandboxReadOnly, SandboxWorkspaceWrite, SandboxDangerFullAccess:
		return true
	}
	return false
}

// IsMutatingAllowed reports whether a tool may run in this mode. Read-only
// mode keeps only the known safe tools; every other mode leaves the decision
// to the policy gate and, for workspace-write, to InWriteRoots.
func (m SandboxMode) IsMutatingAllowed(toolName string) bool {
	if m == SandboxReadOnly {
		return defaultReadOnlyTools[toolName]
	}
	return true
}

// InWriteRoots verifies that path stays inside one of the write roots. A
// relative path is resolved against the first root (normally the workspace
// cwd), symlinks are resolved when they exist, and `..` escapes are rejected.
func InWriteRoots(path string, roots []string) error {
	if len(roots) == 0 {
		return fmt.Errorf("security: no write roots configured")
	}
	cleaned := make([]string, 0, len(roots))
	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		absolute, err := filepath.Abs(expandHome(root))
		if err != nil {
			return fmt.Errorf("security: resolving write root %q: %w", root, err)
		}
		cleaned = append(cleaned, filepath.Clean(absolute))
	}
	if len(cleaned) == 0 {
		return fmt.Errorf("security: no write roots configured")
	}
	candidate := expandHome(strings.TrimSpace(path))
	if candidate == "" {
		candidate = "."
	}
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(cleaned[0], candidate)
	}
	absolute, err := filepath.Abs(candidate)
	if err != nil {
		return fmt.Errorf("security: resolving path %q: %w", path, err)
	}
	absolute = filepath.Clean(absolute)
	if within(absolute, cleaned) {
		return nil
	}
	if resolved, err := canonicalWithTail(absolute); err == nil {
		if within(filepath.Clean(resolved), cleaned) {
			return nil
		}
		for i, root := range cleaned {
			if realRoot, err := filepath.EvalSymlinks(root); err == nil {
				cleaned[i] = filepath.Clean(realRoot)
			}
		}
		if within(filepath.Clean(resolved), cleaned) {
			return nil
		}
	}
	return fmt.Errorf("security: path %q is outside the write roots %v", path, roots)
}

func within(path string, roots []string) bool {
	for _, root := range roots {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			continue
		}
		if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
			return true
		}
	}
	return false
}

// WriteRootsFrom is a small helper for CLI wiring: the workspace cwd plus any
// --add-dir directories, deduplicated and made absolute.
func WriteRootsFrom(cwd string, extra []string) []string {
	roots := make([]string, 0, len(extra)+1)
	seen := make(map[string]bool, len(extra)+1)
	add := func(value string) {
		if strings.TrimSpace(value) == "" {
			return
		}
		absolute, err := filepath.Abs(expandHome(value))
		if err != nil {
			return
		}
		absolute = filepath.Clean(absolute)
		if seen[absolute] {
			return
		}
		seen[absolute] = true
		roots = append(roots, absolute)
	}
	add(cwd)
	for _, dir := range extra {
		add(dir)
	}
	if len(roots) == 0 {
		if wd, err := os.Getwd(); err == nil {
			roots = append(roots, filepath.Clean(wd))
		}
	}
	return roots
}
