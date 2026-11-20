//go:build windows

package security

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// The stdlib syscall package on Windows does not expose LockFileEx, so bind it
// directly from kernel32. An OS-level byte-range lock is auto-released when the
// owning process exits, which avoids the stale-lock reclaim problem a purely
// user-space O_EXCL placeholder would need to solve.
const ttlLockExclusive = 0x00000002

var (
	ttlKernel32         = syscall.NewLazyDLL("kernel32.dll")
	ttlProcLockFileEx   = ttlKernel32.NewProc("LockFileEx")
	ttlProcUnlockFileEx = ttlKernel32.NewProc("UnlockFileEx")
)

// ttlFileLock owns the sidecar lock file handle for one critical section.
type ttlFileLock struct {
	f *os.File
}

// ttlAcquireLock opens (creating if needed) path and takes a blocking exclusive
// lock on byte zero of it.
func ttlAcquireLock(path string) (*ttlFileLock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("security: open ttl grants lock: %w", err)
	}
	var ov syscall.Overlapped
	// LockFileEx(hFile, dwFlags, dwReserved, nBytesLow, nBytesHigh, lpOverlapped).
	// Blocking (no LOCKFILE_FAIL_IMMEDIATELY) so concurrent writers queue up.
	r1, _, e1 := ttlProcLockFileEx.Call(
		uintptr(f.Fd()),
		uintptr(ttlLockExclusive),
		0,
		1,
		0,
		uintptr(unsafe.Pointer(&ov)),
	)
	if r1 == 0 {
		f.Close()
		if e1 != syscall.Errno(0) {
			return nil, fmt.Errorf("security: lock ttl grants: %w", e1)
		}
		return nil, fmt.Errorf("security: lock ttl grants: call failed")
	}
	return &ttlFileLock{f: f}, nil
}

// Release drops the byte-range lock and closes the sidecar file.
func (l *ttlFileLock) Release() error {
	var ov syscall.Overlapped
	// UnlockFileEx(hFile, dwReserved, nBytesLow, nBytesHigh, lpOverlapped).
	r1, _, e1 := ttlProcUnlockFileEx.Call(
		uintptr(l.f.Fd()),
		0,
		1,
		0,
		uintptr(unsafe.Pointer(&ov)),
	)
	cerr := l.f.Close()
	if r1 == 0 {
		if e1 != syscall.Errno(0) {
			return fmt.Errorf("security: unlock ttl grants: %w", e1)
		}
		return fmt.Errorf("security: unlock ttl grants: call failed")
	}
	return cerr
}
