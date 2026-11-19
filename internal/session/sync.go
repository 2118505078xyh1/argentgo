package session

import (
	"fmt"
	"os"
)

// Sync flushes the session file's buffered writes to stable storage. It works
// on a freshly opened handle and does not touch the append state or truncate
// the file, so callers may use it as a "durability barrier" before an
// expensive request (see the before-request hook in the agent package).
//
// The file is opened read-only first because that is portable and avoids
// taking a write lock. If the platform rejects Sync on a read-only handle
// (notably Windows, whose FlushFileBuffers can require write access), the
// handle is reopened with O_RDWR — which never truncates — and Sync is tried
// again. Any failure from the final attempt is returned wrapped.
func (s *Session) Sync() error {
	path := s.path()
	fd, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("session: sync open %s: %w", path, err)
	}
	if err := fd.Sync(); err == nil {
		_ = fd.Close()
		return nil
	}
	// Read-only Sync is not accepted on every platform; retry with O_RDWR.
	if err := fd.Close(); err != nil {
		return fmt.Errorf("session: sync close %s: %w", path, err)
	}
	rw, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("session: sync reopen %s: %w", path, err)
	}
	defer rw.Close()
	if err := rw.Sync(); err != nil {
		return fmt.Errorf("session: sync %s: %w", path, err)
	}
	return nil
}
