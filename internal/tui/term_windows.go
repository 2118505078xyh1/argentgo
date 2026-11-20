//go:build windows

package tui

import (
	"os"
	"syscall"
	"time"
	"unsafe"
)

// Console modes from wincon.h. Virtual terminal input is what makes Windows
// deliver escape sequences on stdin instead of raw key records; without it the
// arrow keys never reach the decoder.
const (
	enableProcessedInput            = 0x0001
	enableLineInput                 = 0x0002
	enableEchoInput                 = 0x0004
	enableVirtualTerminalProcessing = 0x0004
	enableVirtualTerminalInput      = 0x0200
	enableMouseInput                = 0x0010 // ENABLE_MOUSE_INPUT: emit SGR mouse reports on stdin
)

var (
	kernel32                       = syscall.NewLazyDLL("kernel32.dll")
	procGetConsoleMode             = kernel32.NewProc("GetConsoleMode")
	procSetConsoleMode             = kernel32.NewProc("SetConsoleMode")
	procGetConsoleScreenBufferInfo = kernel32.NewProc("GetConsoleScreenBufferInfo")
)

type consoleCoord struct {
	X int16
	Y int16
}

type consoleRect struct {
	Left   int16
	Top    int16
	Right  int16
	Bottom int16
}

type consoleScreenBufferInfo struct {
	Size              consoleCoord
	CursorPosition    consoleCoord
	Attributes        uint16
	Window            consoleRect
	MaximumWindowSize consoleCoord
}

func consoleMode(handle syscall.Handle) (uint32, bool) {
	var mode uint32
	ok, _, _ := procGetConsoleMode.Call(uintptr(handle), uintptr(unsafe.Pointer(&mode)))
	return mode, ok != 0
}

func setConsoleMode(handle syscall.Handle, mode uint32) bool {
	ok, _, _ := procSetConsoleMode.Call(uintptr(handle), uintptr(mode))
	return ok != 0
}

// enableRaw switches the console to virtual-terminal input with echo and line
// buffering off, so single keys arrive immediately.
func enableRaw() (func(), error) {
	in := syscall.Handle(os.Stdin.Fd())
	out := syscall.Handle(os.Stdout.Fd())
	inMode, okIn := consoleMode(in)
	outMode, okOut := consoleMode(out)
	if !okIn || !okOut {
		return nil, errNotConsole
	}
	raw := inMode &^ (enableEchoInput | enableLineInput | enableProcessedInput)
	raw |= enableVirtualTerminalInput | enableMouseInput
	if !setConsoleMode(in, raw) {
		return nil, errNotConsole
	}
	setConsoleMode(out, outMode|enableVirtualTerminalProcessing)
	return func() {
		setConsoleMode(in, inMode)
		setConsoleMode(out, outMode)
	}, nil
}

// terminalSize reads the visible window rectangle.
func terminalSize() (int, int) {
	out := syscall.Handle(os.Stdout.Fd())
	var info consoleScreenBufferInfo
	ok, _, _ := procGetConsoleScreenBufferInfo.Call(uintptr(out), uintptr(unsafe.Pointer(&info)))
	if ok == 0 {
		return 0, 0
	}
	width := int(info.Window.Right-info.Window.Left) + 1
	height := int(info.Window.Bottom-info.Window.Top) + 1
	return width, height
}

// watchResize polls the console size: Windows has no SIGWINCH.
func watchResize() (<-chan size, func()) {
	out := make(chan size, 4)
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		lastW, lastH := terminalSize()
		for {
			select {
			case <-ticker.C:
				w, h := terminalSize()
				if w > 0 && h > 0 && (w != lastW || h != lastH) {
					lastW, lastH = w, h
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
	return out, func() { close(done) }
}
