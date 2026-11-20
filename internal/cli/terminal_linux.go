//go:build linux

package cli

import (
	"syscall"
	"unsafe"
)

// ioctlReadTermios 在 Linux 上是 TCGETS；ioctl 成功即表示 fd 是 tty。
const ioctlReadTermios = 0x5401

func isTerminal(fd uintptr) bool {
	var termios syscall.Termios
	_, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, fd, ioctlReadTermios,
		uintptr(unsafe.Pointer(&termios)), 0, 0, 0)
	return errno == 0
}
