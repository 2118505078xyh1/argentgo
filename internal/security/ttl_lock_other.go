//go:build !windows

package security

import (
	"fmt"
	"os"
	"syscall"
)

// ttlFileLock 为一个临界区持有 sidecar 锁文件的句柄。
type ttlFileLock struct {
	f *os.File
}

// ttlAcquireLock 打开（必要时创建）path，并用 flock(2) 获取一个阻塞的独占
// 建议锁。进程结束时内核会释放它，因此崩溃不会留下陈旧锁。
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

// Release 释放建议锁并关闭 sidecar 文件。
func (l *ttlFileLock) Release() error {
	err := syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	cerr := l.f.Close()
	if err != nil {
		return fmt.Errorf("security: unlock ttl grants: %w", err)
	}
	return cerr
}
