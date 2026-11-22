//go:build windows

package session

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// Windows 标准库的 syscall 包没有暴露 LockFileEx，因此直接从 kernel32 绑定。
// 系统级字节范围锁在持有它的进程退出时由操作系统释放，避免了用户态
// O_EXCL 占位文件必须解决且容易出错的陈旧锁回收问题。
const appendLockExclusive = 0x00000002

var (
	appendKernel32         = syscall.NewLazyDLL("kernel32.dll")
	appendProcLockFileEx   = appendKernel32.NewProc("LockFileEx")
	appendProcUnlockFileEx = appendKernel32.NewProc("UnlockFileEx")
)

// appendFileLock 为一个临界区持有 sidecar 锁文件的句柄。
type appendFileLock struct {
	f *os.File
}

// appendAcquireLock 打开（必要时创建）path，并在其第 0 字节上获取阻塞的独占锁。
// 不带 LOCKFILE_FAIL_IMMEDIATELY，因此并发写入方会排队等待。
func appendAcquireLock(path string) (*appendFileLock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("session: open append lock file: %w", err)
	}
	var ov syscall.Overlapped
	// LockFileEx(hFile, dwFlags, dwReserved, nBytesLow, nBytesHigh, lpOverlapped)。
	r1, _, e1 := appendProcLockFileEx.Call(
		uintptr(f.Fd()),
		uintptr(appendLockExclusive),
		0,
		1,
		0,
		uintptr(unsafe.Pointer(&ov)),
	)
	if r1 == 0 {
		f.Close()
		if e1 != syscall.Errno(0) {
			return nil, fmt.Errorf("session: lock append file: %w", e1)
		}
		return nil, fmt.Errorf("session: lock append file: call failed")
	}
	return &appendFileLock{f: f}, nil
}

// Release 释放字节范围锁并关闭 sidecar 文件。
func (l *appendFileLock) Release() error {
	var ov syscall.Overlapped
	// UnlockFileEx(hFile, dwReserved, nBytesLow, nBytesHigh, lpOverlapped)。
	r1, _, e1 := appendProcUnlockFileEx.Call(
		uintptr(l.f.Fd()),
		0,
		1,
		0,
		uintptr(unsafe.Pointer(&ov)),
	)
	cerr := l.f.Close()
	if r1 == 0 {
		if e1 != syscall.Errno(0) {
			return fmt.Errorf("session: unlock append file: %w", e1)
		}
		return fmt.Errorf("session: unlock append file: call failed")
	}
	return cerr
}
