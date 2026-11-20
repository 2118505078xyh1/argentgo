//go:build linux || darwin

package tui

import (
	"os"
	"os/signal"
	"syscall"
	"unsafe"
)

type winsize struct {
	Row    uint16
	Col    uint16
	Xpixel uint16
	Ypixel uint16
}

func ioctl(fd, request uintptr, arg unsafe.Pointer) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, request, uintptr(arg))
	if errno != 0 {
		return errno
	}
	return nil
}

// enableRaw 把终端置入原始模式，使每次按键立即
// 到达且没有回显进入备用屏幕。
func enableRaw() (func(), error) {
	fd := os.Stdin.Fd()
	var original syscall.Termios
	if err := ioctl(fd, uintptr(ioctlGetTermios), unsafe.Pointer(&original)); err != nil {
		return nil, err
	}
	raw := original
	raw.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP |
		syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON
	raw.Oflag &^= syscall.OPOST
	raw.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	raw.Cflag &^= syscall.CSIZE | syscall.PARENB
	raw.Cflag |= syscall.CS8
	raw.Cc[syscall.VMIN] = 1
	raw.Cc[syscall.VTIME] = 0
	if err := ioctl(fd, uintptr(ioctlSetTermios), unsafe.Pointer(&raw)); err != nil {
		return nil, err
	}
	return func() {
		_ = ioctl(fd, uintptr(ioctlSetTermios), unsafe.Pointer(&original))
	}, nil
}

// terminalSize 以单元格报告窗口大小。
func terminalSize() (int, int) {
	var ws winsize
	if err := ioctl(os.Stdout.Fd(), uintptr(ioctlGetWinsize), unsafe.Pointer(&ws)); err != nil {
		return 0, 0
	}
	return int(ws.Col), int(ws.Row)
}

// watchResize 把 SIGWINCH 转换为几何通知。
func watchResize() (<-chan size, func()) {
	notify := make(chan os.Signal, 4)
	signal.Notify(notify, syscall.SIGWINCH)
	out := make(chan size, 4)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-notify:
				if w, h := terminalSize(); w > 0 && h > 0 {
					select {
					case out <- size{w: w, h: h}:
					default:
					}
				}
			case <-done:
				return
			}
		}
	}()
	stop := func() {
		signal.Stop(notify)
		close(done)
	}
	return out, stop
}
