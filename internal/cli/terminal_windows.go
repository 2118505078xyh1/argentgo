//go:build windows

package cli

import (
	"syscall"
	"unsafe"
)

var (
	kernel32DLL        = syscall.NewLazyDLL("kernel32.dll")
	procGetConsoleMode = kernel32DLL.NewProc("GetConsoleMode")
)

// isTerminal 向 Windows 控制台 API 询问 fd 是否是交互式控制台句柄。
// 非控制台句柄（管道、文件）会使调用失败。
func isTerminal(fd uintptr) bool {
	var mode uint32
	r, _, _ := procGetConsoleMode.Call(fd, uintptr(unsafe.Pointer(&mode)))
	return r != 0
}
