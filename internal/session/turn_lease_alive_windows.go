//go:build windows

package session

import (
	"syscall"
	"unsafe"
)

const (
	leaseProcessQueryLimitedInfo = 0x1000
	leaseStillActive             = 259
)

var (
	leaseKernel32        = syscall.NewLazyDLL("kernel32.dll")
	leaseProcOpenProcess = leaseKernel32.NewProc("OpenProcess")
	leaseProcGetExitCode = leaseKernel32.NewProc("GetExitCodeProcess")
	leaseProcCloseHandle = leaseKernel32.NewProc("CloseHandle")
)

// leasePIDAlive 用 OpenProcess + GetExitCodeProcess 判断本机 PID 是否存活。
// 与 internal/proc 的存活判定同构：句柄查询失败视为已死，STILL_ACTIVE 视为存活。
func leasePIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, _, _ := leaseProcOpenProcess.Call(uintptr(leaseProcessQueryLimitedInfo), 0, uintptr(pid))
	if handle == 0 || handle == ^uintptr(0) {
		return false
	}
	defer leaseProcCloseHandle.Call(handle)
	var code uint32
	r, _, _ := leaseProcGetExitCode.Call(handle, uintptr(unsafe.Pointer(&code)))
	return r != 0 && code == leaseStillActive
}
