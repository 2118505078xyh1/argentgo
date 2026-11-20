//go:build !windows

package security

import "path/filepath"

// realPathExisting resolves symlinks on POSIX systems. filepath.EvalSymlinks
// follows the whole chain; there are no directory junctions to account for.
func realPathExisting(path string) (string, error) {
	return filepath.EvalSymlinks(path)
}
