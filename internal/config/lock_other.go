//go:build !windows

package config

import (
	"fmt"
	"os"
	"syscall"
)

// configFileLock 为一个临界区持有 sidecar 锁文件的句柄。
type configFileLock struct {
	f *os.File
}

// configAcquireLock 打开（必要时创建）path，并用 flock(2) 获取阻塞的
// 独占建议锁。进程结束时内核释放该锁，因此崩溃不会留下陈旧锁。
func configAcquireLock(path string) (*configFileLock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("config: open lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("config: lock file: %w", err)
	}
	return &configFileLock{f: f}, nil
}

// Release 释放建议锁并关闭 sidecar 文件。
func (l *configFileLock) Release() error {
	err := syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	cerr := l.f.Close()
	if err != nil {
		return fmt.Errorf("config: unlock file: %w", err)
	}
	return cerr
}
