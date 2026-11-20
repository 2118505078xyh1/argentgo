//go:build darwin

package pty

import (
	"fmt"
	"syscall"
	"unsafe"
)

// macOS 通过 /dev/ptmx 暴露 pty 但操作命名
// 不同：grant、unlock，然后读回从设备路径。
const (
	ioctlTIOCPTYGRANT = 0x2000_7454
	ioctlTIOCPTYUNLK  = 0x2000_7452
	ioctlTIOCPTYGNAME = 0x4080_7453
	ioctlTIOCSWINSZ   = 0x8008_7467
)

// macOS 通过 /dev/ptmx 暴露 pty 但操作命名不同：grant、unlock，然后读回从设备路径。winsize 镜像 termios.h 的 struct winsize。unlockMaster 清除 pty 锁并返回从设备路径。winsize 镜像 termios.h 的 struct winsize。unlockMaster 清除 pty 锁并返回从设备路径。
type winsize struct {
	rows uint16
	cols uint16
	x    uint16
	y    uint16
}

// unlockMaster 清除 pty 锁并返回从设备路径。
func unlockMaster(fd uintptr) (string, error) {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, ioctlTIOCPTYGRANT, 0); errno != 0 {
		return "", fmt.Errorf("pty: TIOCPTYGRANT: %w", errno)
	}
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, ioctlTIOCPTYUNLK, 0); errno != 0 {
		return "", fmt.Errorf("pty: TIOCPTYUNLK: %w", errno)
	}
	var name [128]byte
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, ioctlTIOCPTYGNAME, uintptr(unsafe.Pointer(&name[0]))); errno != 0 {
		return "", fmt.Errorf("pty: TIOCPTYGNAME: %w", errno)
	}
	length := 0
	for length < len(name) && name[length] != 0 {
		length++
	}
	if length == 0 {
		return "", fmt.Errorf("pty: TIOCPTYGNAME returned an empty path")
	}
	return string(name[:length]), nil
}

func setWinsize(fd uintptr, size Size) error {
	window := winsize{rows: size.Rows, cols: size.Cols}
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, ioctlTIOCSWINSZ, uintptr(unsafe.Pointer(&window))); errno != 0 {
		return fmt.Errorf("pty: TIOCSWINSZ: %w", errno)
	}
	return nil
}
