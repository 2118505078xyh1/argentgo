//go:build darwin

package cli

import (
	"syscall"
	"unsafe"
)

// ioctlReadTermios is TIOCGETA on darwin; a successful ioctl means fd is a tty.
const ioctlReadTermios = 0x40487413

func isTerminal(fd uintptr) bool {
	var termios syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, ioctlReadTermios,
		uintptr(unsafe.Pointer(&termios)))
	return errno == 0
}
