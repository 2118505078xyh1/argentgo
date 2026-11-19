//go:build linux

package pty

import (
	"fmt"
	"syscall"
	"unsafe"
)

const (
	ioctlTIOCSPTLCK = 0x4004_5431
	ioctlTIOCGPTN   = 0x8004_5430
	ioctlTIOCSWINSZ = 0x5414
)

// winsize mirrors struct winsize from termios.h.
type winsize struct {
	rows uint16
	cols uint16
	x    uint16
	y    uint16
}

// unlockMaster clears the pty lock and returns the slave device path.
func unlockMaster(fd uintptr) (string, error) {
	var unlock int32
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, ioctlTIOCSPTLCK, uintptr(unsafe.Pointer(&unlock))); errno != 0 {
		return "", fmt.Errorf("pty: TIOCSPTLCK: %w", errno)
	}
	var number uint32
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, ioctlTIOCGPTN, uintptr(unsafe.Pointer(&number))); errno != 0 {
		return "", fmt.Errorf("pty: TIOCGPTN: %w", errno)
	}
	return fmt.Sprintf("/dev/pts/%d", number), nil
}

func setWinsize(fd uintptr, size Size) error {
	window := winsize{rows: size.Rows, cols: size.Cols}
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, ioctlTIOCSWINSZ, uintptr(unsafe.Pointer(&window))); errno != 0 {
		return fmt.Errorf("pty: TIOCSWINSZ: %w", errno)
	}
	return nil
}
