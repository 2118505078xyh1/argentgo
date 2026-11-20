//go:build windows

package security

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"
)

var (
	finalPathKernel32 = syscall.NewLazyDLL("kernel32.dll")
	finalPathProc     = finalPathKernel32.NewProc("GetFinalPathNameByHandleW")
)

// FILE_NAME_NORMALIZED | VOLUME_NAME_DOS. The DOS form is returned as
// \\?\C:\... and trimmed below so it compares cleanly with lexical paths.
const finalPathNameFlags = 0x00000000

// realPathExisting asks the kernel where an already-open handle really lives.
// os.Open follows directory junctions (it does not pass
// FILE_FLAG_OPEN_REPARSE_POINT), while filepath.EvalSymlinks on Windows leaves
// junctions untouched, so this is the only reliable stdlib-only resolution.
func realPathExisting(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	resolved, err := finalPathNameByHandle(syscall.Handle(file.Fd()))
	if err != nil {
		// Backstop for the unlikely case where the kernel call is unavailable:
		// never let an unresolved reparse point through.
		if hasReparsePoint(path) {
			return "", fmt.Errorf("security: reparse point %q could not be resolved on Windows: %w", path, err)
		}
		return "", err
	}
	if hasReparsePoint(resolved) {
		return "", fmt.Errorf("security: resolved path %q is still a reparse point; refusing", resolved)
	}
	return resolved, nil
}

func finalPathNameByHandle(handle syscall.Handle) (string, error) {
	if err := finalPathProc.Find(); err != nil {
		return "", fmt.Errorf("security: GetFinalPathNameByHandleW unavailable: %w", err)
	}
	// A zero-size buffer makes the API report the required length in WCHARs,
	// including the terminating NUL. A non-zero return is expected; the last
	// error at this point is ERROR_INSUFFICIENT_BUFFER and is ignored.
	size, _, _ := finalPathProc.Call(uintptr(handle), 0, 0, finalPathNameFlags)
	if size == 0 {
		return "", fmt.Errorf("security: GetFinalPathNameByHandleW returned no length")
	}
	buf := make([]uint16, size+1)
	n, _, callErr := finalPathProc.Call(
		uintptr(handle), uintptr(unsafe.Pointer(&buf[0])), size, finalPathNameFlags)
	if n == 0 {
		if callErr != nil {
			return "", callErr
		}
		return "", fmt.Errorf("security: GetFinalPathNameByHandleW failed")
	}
	return trimExtendedPath(syscall.UTF16ToString(buf)), nil
}

func trimExtendedPath(path string) string {
	if strings.HasPrefix(path, `\\?\UNC\`) {
		return `\\` + path[len(`\\?\UNC\`):]
	}
	if strings.HasPrefix(path, `\\?\`) {
		return path[len(`\\?\`):]
	}
	return path
}

// hasReparsePoint reports whether the final component of path is a symlink or
// a Windows reparse point (junctions, mounts, dedup links). It is a fail-closed
// backstop, not the primary resolution mechanism.
func hasReparsePoint(path string) bool {
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	return isLinkInfo(info)
}

// isLinkInfo reports whether info describes a symlink or any other Windows
// reparse point (junctions, mounts, dedup links). It is shared with linkTarget
// so a dangling junction is recognised by the same rule as every other
// reparse-point check in this package.
func isLinkInfo(info os.FileInfo) bool {
	if info == nil {
		return false
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return true
	}
	if data, ok := info.Sys().(*syscall.Win32FileAttributeData); ok {
		return data.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
	}
	return false
}

// normalizeLinkTarget strips the NT (\??\) and extended-length (\\?\) prefixes
// os.Readlink may return on Windows so the target compares cleanly with lexical
// paths. Without the NT form, filepath.IsAbs would treat the target as relative
// and join it under the link's directory, hiding a real escape.
func normalizeLinkTarget(path string) string {
	if strings.HasPrefix(path, `\??\UNC\`) {
		return `\\` + path[len(`\??\UNC\`):]
	}
	if strings.HasPrefix(path, `\??\`) {
		return path[len(`\??\`):]
	}
	return trimExtendedPath(path)
}
