//go:build linux

package tui

// Linux ioctl 请求：TCGETS、TCSETS、TIOCGWINSZ。
const (
	ioctlGetTermios = 0x5401
	ioctlSetTermios = 0x5402
	ioctlGetWinsize = 0x5413
)
