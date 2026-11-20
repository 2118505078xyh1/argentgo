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

// isTerminal asks the Windows console API whether fd is an interactive
// console handle. Non-console handles (pipes, files) make the call fail.
func isTerminal(fd uintptr) bool {
	var mode uint32
	r, _, _ := procGetConsoleMode.Call(fd, uintptr(unsafe.Pointer(&mode)))
	return r != 0
}
