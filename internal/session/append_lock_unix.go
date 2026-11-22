//go:build unix

package session

import (
	"fmt"
	"os"
	"syscall"
)

// appendFileLock 为一个临界区持有 sidecar 锁文件的句柄。
type appendFileLock struct {
	f *os.File
}

// appendAcquireLock 打开（必要时创建）path，并用 flock(2) 获取阻塞的独占建议锁。
// 进程结束时内核释放该锁，因此崩溃不会留下陈旧锁。
func appendAcquireLock(path string) (*appendFileLock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("session: open append lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("session: lock append file: %w", err)
	}
	return &appendFileLock{f: f}, nil
}

// Release 释放建议锁并关闭 sidecar 文件。
func (l *appendFileLock) Release() error {
	err := syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	cerr := l.f.Close()
	if err != nil {
		return fmt.Errorf("session: unlock append file: %w", err)
	}
	return cerr
}
