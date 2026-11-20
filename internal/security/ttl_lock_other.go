//go:build !windows

package security

import (
	"fmt"
	"os"
	"syscall"
)

// ttlFileLock owns the sidecar lock file handle for one critical section.
type ttlFileLock struct {
	f *os.File
}

// ttlAcquireLock opens (creating if needed) path and takes a blocking exclusive
// advisory lock with flock(2). The kernel releases it when the process dies,
// so a crash cannot leave a stale lock behind.
func ttlAcquireLock(path string) (*ttlFileLock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("security: open ttl grants lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("security: lock ttl grants: %w", err)
	}
	return &ttlFileLock{f: f}, nil
}

// Release drops the advisory lock and closes the sidecar file.
func (l *ttlFileLock) Release() error {
	err := syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	cerr := l.f.Close()
	if err != nil {
		return fmt.Errorf("security: unlock ttl grants: %w", err)
	}
	return cerr
}
