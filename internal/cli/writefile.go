package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// writeFileNoFollow writes data to path without following a symbolic link at
// the target path. It is a drop-in, stdlib-only replacement for os.WriteFile
// used by the CLI's export paths (sessions export -o, bug --out).
//
// Protection is layered:
//   - an existing symlink at path is rejected before the open;
//   - on Unix the open itself uses O_NOFOLLOW, so a link swapped in between
//     the check and the create is rejected atomically by the kernel;
//   - after closing, path is checked again so a swap is detected rather than
//     silently accepted.
//
// On platforms without a portable O_NOFOLLOW (Windows and others) the Lstat
// guards are the only protection; they still block the reported attack where
// the user points an export at a pre-existing symlink such as ~/.bashrc.
// Windows junctions and other reparse points surface as os.ModeIrregular and
// are rejected as well.
func writeFileNoFollow(path string, data []byte, perm os.FileMode) error {
	if fi, err := os.Lstat(path); err == nil {
		if isNoFollowViolation(fi.Mode()) {
			return fmt.Errorf("refusing to write through symlink or reparse point %q", path)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	f, err := openNoFollowFile(path, perm)
	if err != nil {
		return err
	}
	n, writeErr := f.Write(data)
	if writeErr == nil && n < len(data) {
		writeErr = io.ErrShortWrite
	}
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}

	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if isNoFollowViolation(fi.Mode()) {
		return fmt.Errorf("refusing to write through symlink or reparse point %q", path)
	}
	return nil
}

// isNoFollowViolation reports whether a path's mode marks it as a link that
// must never be written through. ModeIrregular covers Windows junctions and
// other reparse points; Unix Lstat never sets it for ordinary files.
func isNoFollowViolation(mode os.FileMode) bool {
	return mode&(os.ModeSymlink|os.ModeIrregular) != 0
}
