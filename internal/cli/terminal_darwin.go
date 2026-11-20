//go:build darwin

package cli

import (
	"syscall"
	"unsafe"
)

// ioctlReadTermios 在 darwin 上是 TIOCGETA；ioctl 成功即表示 fd 是 tty。
const ioctlReadTermios = 0x40487413

func isTerminal(fd uintptr) bool {
	var termios syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, ioctlReadTermios,
		uintptr(unsafe.Pointer(&termios)))
	return errno == 0
}
