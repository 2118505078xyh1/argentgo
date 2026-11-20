//go:build !unix

package cli

import "os"

// openNoFollowFile falls back to a plain open where O_NOFOLLOW is not portable
// (Windows and other non-unix targets). writeFileNoFollow's Lstat guards are
// the symlink defence there; Go reports Windows symlinks as os.ModeSymlink.
func openNoFollowFile(path string, perm os.FileMode) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
}
