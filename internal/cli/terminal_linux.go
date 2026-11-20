//go:build linux

package cli

import (
	"syscall"
	"unsafe"
)

// ioctlReadTermios is TCGETS on Linux; a successful ioctl means fd is a tty.
const ioctlReadTermios = 0x5401

func isTerminal(fd uintptr) bool {
	var termios syscall.Termios
	_, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, fd, ioctlReadTermios,
		uintptr(unsafe.Pointer(&termios)), 0, 0, 0)
	return errno == 0
}
