//go:build unix

package cli

import (
	"os"
	"syscall"
)

// openNoFollowFile opens path for writing with O_NOFOLLOW so a symlink at the
// final path component is never followed. Only permission bits are passed to
// the syscall; umask still applies, matching os.Create/os.WriteFile semantics.
func openNoFollowFile(path string, perm os.FileMode) (*os.File, error) {
	fd, err := syscall.Open(path,
		syscall.O_WRONLY|syscall.O_CREAT|syscall.O_TRUNC|syscall.O_NOFOLLOW,
		uint32(perm.Perm()))
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}
