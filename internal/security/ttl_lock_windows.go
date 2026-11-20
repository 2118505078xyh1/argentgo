//go:build windows

package security

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// Windows 上标准库 syscall 包没有暴露 LockFileEx，因此直接从 kernel32 绑定它。
// 操作系统级的字节范围锁会在拥有它的进程退出时自动释放，这避免了纯用户态
// O_EXCL 占位文件需要解决的陈旧锁回收问题。
const ttlLockExclusive = 0x00000002

var (
	ttlKernel32         = syscall.NewLazyDLL("kernel32.dll")
	ttlProcLockFileEx   = ttlKernel32.NewProc("LockFileEx")
	ttlProcUnlockFileEx = ttlKernel32.NewProc("UnlockFileEx")
)

// ttlFileLock 为一个临界区持有 sidecar 锁文件的句柄。
type ttlFileLock struct {
	f *os.File
}

// ttlAcquireLock 打开（必要时创建）path，并在其第 0 字节上获取一个阻塞的
// 独占锁。
func ttlAcquireLock(path string) (*ttlFileLock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("security: open ttl grants lock: %w", err)
	}
	var ov syscall.Overlapped
	// LockFileEx(hFile, dwFlags, dwReserved, nBytesLow, nBytesHigh, lpOverlapped)。
	// 阻塞式的（不带 LOCKFILE_FAIL_IMMEDIATELY），使并发写入者排队等待。
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

// Release 释放字节范围锁并关闭 sidecar 文件。
func (l *ttlFileLock) Release() error {
	var ov syscall.Overlapped
	// UnlockFileEx(hFile, dwReserved, nBytesLow, nBytesHigh, lpOverlapped)。
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
