//go:build darwin

package tui

// Darwin ioctl 请求：TIOCGETA、TIOCSETA、TIOCGWINSZ。
const (
	ioctlGetTermios = 0x40487413
	ioctlSetTermios = 0x80487414
	ioctlGetWinsize = 0x40087468
)
