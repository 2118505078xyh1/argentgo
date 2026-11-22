//go:build !unix && !windows

package session

// appendFileLock 在无跨进程文件锁能力的平台上的空操作实现。
// 它保留与 unix/windows 版本一致的方法签名，使 withAppendLock 无需分支。
// 进程内多个句柄的串行化仍由调用方持有的 s.mu 保证。
type appendFileLock struct{}

// appendAcquireLock 不获取任何系统级锁，直接返回一个空操作锁。
func appendAcquireLock(path string) (*appendFileLock, error) {
	return &appendFileLock{}, nil
}

// Release 是无操作。
func (l *appendFileLock) Release() error { return nil }
