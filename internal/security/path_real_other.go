//go:build !windows

package security

import (
	"os"
	"path/filepath"
)

// realPathExisting resolves symlinks on POSIX systems. filepath.EvalSymlinks
// follows the whole chain; there are no directory junctions to account for.
func realPathExisting(path string) (string, error) {
	return filepath.EvalSymlinks(path)
}

// isLinkInfo reports whether info describes a symlink. POSIX has no junctions,
// so the mode bit is the only link marker needed.
func isLinkInfo(info os.FileInfo) bool {
	return info != nil && info.Mode()&os.ModeSymlink != 0
}

// normalizeLinkTarget is a no-op on POSIX: Readlink returns the target as-is.
func normalizeLinkTarget(path string) string { return path }
