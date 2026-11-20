//go:build windows

package pty

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

// ConPTY is available from Windows 10 1809 (build 17763) on. Everything here
// goes through kernel32 directly: no cgo, no gcc at build time.
const (
	extendedStartupInfoPresent = 0x0008_0000
	startfUseStdHandles        = 0x0000_0100
	createUnicodeEnvironment   = 0x0000_0400
	pseudoConsoleAttribute     = 0x0002_0016
	infinite                   = 0xFFFF_FFFF
	waitObject0                = 0x0000_0000
	waitFailed                 = 0xFFFF_FFFF
)

var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	procCreatePipe                        = kernel32.NewProc("CreatePipe")
	procCreatePseudoConsole               = kernel32.NewProc("CreatePseudoConsole")
	procResizePseudoConsole               = kernel32.NewProc("ResizePseudoConsole")
	procClosePseudoConsole                = kernel32.NewProc("ClosePseudoConsole")
	procInitializeProcThreadAttributeList = kernel32.NewProc("InitializeProcThreadAttributeList")
	procUpdateProcThreadAttribute         = kernel32.NewProc("UpdateProcThreadAttribute")
	procDeleteProcThreadAttributeList     = kernel32.NewProc("DeleteProcThreadAttributeList")
	procCreateProcessW                    = kernel32.NewProc("CreateProcessW")
	procGetExitCodeProcess                = kernel32.NewProc("GetExitCodeProcess")
	procTerminateProcess                  = kernel32.NewProc("TerminateProcess")
	procWaitForSingleObject               = kernel32.NewProc("WaitForSingleObject")
)

// coord is COORD: a 4-byte struct the Win32 ABI passes by value, so it travels
// as one packed integer argument rather than a pointer.
func coordOf(size Size) uintptr {
	return uintptr(uint32(uint16(size.Cols)) | uint32(uint16(size.Rows))<<16)
}

// failed reports a failing HRESULT.
func failed(result uintptr) bool { return int32(uint32(result)) < 0 }

type startupInfo struct {
	cb            uint32
	reserved      *uint16
	desktop       *uint16
	title         *uint16
	x             uint32
	y             uint32
	xSize         uint32
	ySize         uint32
	xCountChars   uint32
	yCountChars   uint32
	fillAttribute uint32
	flags         uint32
	showWindow    uint16
	reserved2     uint16
	reserved2Ptr  *byte
	stdInput      syscall.Handle
	stdOutput     syscall.Handle
	stdError      syscall.Handle
}

type startupInfoEx struct {
	startupInfo   startupInfo
	attributeList uintptr
}

type processInformation struct {
	process   syscall.Handle
	thread    syscall.Handle
	processID uint32
	threadID  uint32
}

func supported() bool {
	return procCreatePseudoConsole.Find() == nil &&
		procResizePseudoConsole.Find() == nil &&
		procClosePseudoConsole.Find() == nil
}

// conPty owns one HPCON and the child attached to it.
type conPty struct {
	console syscall.Handle
	process syscall.Handle
	stdin   *os.File
	stdout  *os.File
	pid     int

	mu       sync.Mutex
	closed   bool
	reaped   bool
	exitCode int
}

func startPlatform(cmd Command, size Size) (terminal, error) {
	path, err := resolve(cmd.Path)
	if err != nil {
		return nil, err
	}

	var inRead, inWrite, outRead, outWrite syscall.Handle
	if r, _, callErr := procCreatePipe.Call(
		uintptr(unsafe.Pointer(&inRead)), uintptr(unsafe.Pointer(&inWrite)), 0, 0,
	); r == 0 {
		return nil, fmt.Errorf("pty: CreatePipe: %w", callErr)
	}
	if r, _, callErr := procCreatePipe.Call(
		uintptr(unsafe.Pointer(&outRead)), uintptr(unsafe.Pointer(&outWrite)), 0, 0,
	); r == 0 {
		syscall.CloseHandle(inRead)
		syscall.CloseHandle(inWrite)
		return nil, fmt.Errorf("pty: CreatePipe: %w", callErr)
	}

	window := coordOf(size)
	var console syscall.Handle
	if r, _, callErr := procCreatePseudoConsole.Call(
		window,
		uintptr(inRead),
		uintptr(outWrite),
		0,
		uintptr(unsafe.Pointer(&console)),
	); failed(r) {
		syscall.CloseHandle(inRead)
		syscall.CloseHandle(inWrite)
		syscall.CloseHandle(outRead)
		syscall.CloseHandle(outWrite)
		return nil, fmt.Errorf("pty: CreatePseudoConsole: %w", callErr)
	}
	// The console duplicated these; ours must go or the child never sees EOF.
	syscall.CloseHandle(inRead)
	syscall.CloseHandle(outWrite)

	attributeList, deleteList, err := newAttributeList(console)
	if err != nil {
		procClosePseudoConsole.Call(uintptr(console))
		syscall.CloseHandle(inWrite)
		syscall.CloseHandle(outRead)
		return nil, err
	}
	defer deleteList()

	commandLine, err := syscall.UTF16FromString(buildCommandLine(path, cmd.Args))
	if err != nil {
		procClosePseudoConsole.Call(uintptr(console))
		syscall.CloseHandle(inWrite)
		syscall.CloseHandle(outRead)
		return nil, fmt.Errorf("pty: command line: %w", err)
	}

	var cwdPtr *uint16
	if cmd.Dir != "" {
		cwdPtr, err = syscall.UTF16PtrFromString(cmd.Dir)
		if err != nil {
			procClosePseudoConsole.Call(uintptr(console))
			syscall.CloseHandle(inWrite)
			syscall.CloseHandle(outRead)
			return nil, fmt.Errorf("pty: working directory: %w", err)
		}
	}

	flags := uint32(extendedStartupInfoPresent)
	var envPtr *uint16
	if len(cmd.Env) > 0 {
		envPtr, err = environmentBlock(append(os.Environ(), cmd.Env...))
		if err != nil {
			procClosePseudoConsole.Call(uintptr(console))
			syscall.CloseHandle(inWrite)
			syscall.CloseHandle(outRead)
			return nil, err
		}
		flags |= createUnicodeEnvironment
	}

	startup := startupInfoEx{attributeList: attributeList}
	startup.startupInfo.cb = uint32(unsafe.Sizeof(startup))
	// The console supplies stdio; asking for explicit handles (even null ones)
	// makes the CRT open the inherited console instead of the pseudo one.
	startup.startupInfo.flags = startfUseStdHandles

	var info processInformation
	created, _, callErr := procCreateProcessW.Call(
		0,
		uintptr(unsafe.Pointer(&commandLine[0])),
		0,
		0,
		0, // the pty handles are inherited by the console, not by the child
		uintptr(flags),
		uintptr(unsafe.Pointer(envPtr)),
		uintptr(unsafe.Pointer(cwdPtr)),
		uintptr(unsafe.Pointer(&startup)),
		uintptr(unsafe.Pointer(&info)),
	)
	if created == 0 {
		procClosePseudoConsole.Call(uintptr(console))
		syscall.CloseHandle(inWrite)
		syscall.CloseHandle(outRead)
		return nil, fmt.Errorf("pty: CreateProcessW %q: %w", path, callErr)
	}
	syscall.CloseHandle(info.thread)

	term := &conPty{
		console:  console,
		process:  info.process,
		stdin:    os.NewFile(uintptr(inWrite), "conpty-in"),
		stdout:   os.NewFile(uintptr(outRead), "conpty-out"),
		pid:      int(info.processID),
		exitCode: -1,
	}
	if term.stdin == nil || term.stdout == nil {
		_ = term.Close()
		return nil, errors.New("pty: could not wrap the console pipes")
	}
	return term, nil
}

func newAttributeList(console syscall.Handle) (uintptr, func(), error) {
	var size uintptr
	// The first call only reports the required size.
	procInitializeProcThreadAttributeList.Call(0, 1, 0, uintptr(unsafe.Pointer(&size)))
	if size == 0 {
		return 0, func() {}, errors.New("pty: could not size the attribute list")
	}
	buffer := make([]byte, size)
	list := uintptr(unsafe.Pointer(&buffer[0]))
	if r, _, callErr := procInitializeProcThreadAttributeList.Call(
		list, 1, 0, uintptr(unsafe.Pointer(&size)),
	); r == 0 {
		return 0, func() {}, fmt.Errorf("pty: InitializeProcThreadAttributeList: %w", callErr)
	}
	handle := console
	if r, _, callErr := procUpdateProcThreadAttribute.Call(
		list,
		0,
		pseudoConsoleAttribute,
		uintptr(handle),
		unsafe.Sizeof(handle),
		0,
		0,
	); r == 0 {
		procDeleteProcThreadAttributeList.Call(list)
		return 0, func() {}, fmt.Errorf("pty: UpdateProcThreadAttribute: %w", callErr)
	}
	// The closure keeps the attribute buffer alive until the list is deleted.
	cleanup := func() { procDeleteProcThreadAttributeList.Call(list) }
	return list, cleanup, nil
}

func (c *conPty) Read(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	n, err := c.stdout.Read(b)
	if err != nil && n == 0 && c.Exited() {
		return 0, io.EOF
	}
	return n, err
}

func (c *conPty) Write(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	return c.stdin.Write(b)
}

func (c *conPty) Resize(size Size) error {
	c.mu.Lock()
	console := c.console
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return errors.New("pty: terminal is closed")
	}
	window := coordOf(size)
	if r, _, callErr := procResizePseudoConsole.Call(
		uintptr(console), window,
	); failed(r) {
		return fmt.Errorf("pty: ResizePseudoConsole: %w", callErr)
	}
	return nil
}

func (c *conPty) Wait() (int, error) {
	c.mu.Lock()
	if c.reaped {
		code := c.exitCode
		c.mu.Unlock()
		return code, nil
	}
	process := c.process
	if process == 0 {
		c.mu.Unlock()
		return -1, errors.New("pty: the terminal was closed before the child was reaped")
	}
	// Close may close the original process handle as soon as the lock is
	// released. Use a private duplicate for the whole wait so a concurrent
	// Close+Wait cannot make GetExitCodeProcess fail with "invalid handle"
	// and turn an ordinary exit into a spurious Wait error.
	self, err := syscall.GetCurrentProcess()
	if err != nil {
		c.mu.Unlock()
		return -1, fmt.Errorf("pty: GetCurrentProcess: %w", err)
	}
	var owned syscall.Handle
	if err := syscall.DuplicateHandle(self, process, self, &owned, 0, false, syscall.DUPLICATE_SAME_ACCESS); err != nil {
		c.mu.Unlock()
		return -1, fmt.Errorf("pty: DuplicateHandle: %w", err)
	}
	c.mu.Unlock()
	defer syscall.CloseHandle(owned)

	if r, _, callErr := procWaitForSingleObject.Call(uintptr(owned), infinite); r == waitFailed {
		return -1, fmt.Errorf("pty: WaitForSingleObject: %w", callErr)
	}
	var code uint32
	if r, _, callErr := procGetExitCodeProcess.Call(
		uintptr(owned), uintptr(unsafe.Pointer(&code)),
	); r == 0 {
		return -1, fmt.Errorf("pty: GetExitCodeProcess: %w", callErr)
	}
	c.mu.Lock()
	c.reaped = true
	c.exitCode = int(code)
	c.mu.Unlock()
	return int(code), nil
}

func (c *conPty) Exited() bool {
	c.mu.Lock()
	reaped := c.reaped
	process := c.process
	c.mu.Unlock()
	if reaped || process == 0 {
		return true
	}
	r, _, _ := procWaitForSingleObject.Call(uintptr(process), 0)
	return r == waitObject0
}

func (c *conPty) ExitCode() int {
	c.mu.Lock()
	reaped := c.reaped
	code := c.exitCode
	process := c.process
	c.mu.Unlock()
	if reaped {
		return code
	}
	if process == 0 {
		return -1
	}
	if r, _, _ := procWaitForSingleObject.Call(uintptr(process), 0); r != waitObject0 {
		return -1
	}
	var value uint32
	if r, _, _ := procGetExitCodeProcess.Call(uintptr(process), uintptr(unsafe.Pointer(&value))); r == 0 {
		return -1
	}
	return int(value)
}

func (c *conPty) PID() int { return c.pid }

func (c *conPty) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	process := c.process
	console := c.console
	reaped := c.reaped
	c.mu.Unlock()

	if process != 0 {
		// Reap here so a later Wait still has an exit code to report.
		if !reaped {
			procTerminateProcess.Call(uintptr(process), 1)
			procWaitForSingleObject.Call(uintptr(process), 5000)
			var code uint32
			if r, _, _ := procGetExitCodeProcess.Call(
				uintptr(process), uintptr(unsafe.Pointer(&code)),
			); r != 0 {
				c.mu.Lock()
				c.reaped = true
				c.exitCode = int(code)
				c.mu.Unlock()
			}
		}
	}
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	if c.stdout != nil {
		_ = c.stdout.Close()
	}
	if console != 0 {
		// Blocks until conhost is gone; after TerminateProcess that is quick.
		// ClosePseudoConsole releases the HPCON itself.
		procClosePseudoConsole.Call(uintptr(console))
	}
	if process != 0 {
		// Publish the closed handle under the same lock Wait uses to duplicate
		// it. Otherwise Wait could observe the still-nonzero handle in the
		// window between CloseHandle here and clearing c.process below.
		c.mu.Lock()
		c.process = 0
		syscall.CloseHandle(process)
		c.mu.Unlock()
	}
	return nil
}

// resolve turns a bare program name into a path CreateProcessW can trust.
func resolve(program string) (string, error) {
	if strings.ContainsAny(program, `\/:`) {
		return program, nil
	}
	found, err := exec.LookPath(program)
	if err != nil {
		return "", fmt.Errorf("pty: %q not found: %w", program, err)
	}
	return found, nil
}

// buildCommandLine renders argv into the single string CreateProcessW parses,
// quoting per the MSVC rules a Go or Rust child will undo the same way.
func buildCommandLine(path string, args []string) string {
	var b strings.Builder
	b.WriteString(quoteArg(path))
	for _, arg := range args {
		b.WriteByte(' ')
		b.WriteString(quoteArg(arg))
	}
	return b.String()
}

func quoteArg(arg string) string {
	if arg != "" && !strings.ContainsAny(arg, " \t\n\v\"") {
		return arg
	}
	var b strings.Builder
	b.WriteByte('"')
	slashes := 0
	for _, r := range arg {
		switch r {
		case '\\':
			slashes++
		case '"':
			b.WriteString(strings.Repeat(`\`, slashes*2+1))
			b.WriteRune(r)
			slashes = 0
		default:
			if slashes > 0 {
				b.WriteString(strings.Repeat(`\`, slashes))
				slashes = 0
			}
			b.WriteRune(r)
		}
	}
	if slashes > 0 {
		b.WriteString(strings.Repeat(`\`, slashes*2))
	}
	b.WriteByte('"')
	return b.String()
}

// environmentBlock builds the double-NUL terminated UTF-16 block CreateProcessW
// expects.
func environmentBlock(env []string) (*uint16, error) {
	if len(env) == 0 {
		return nil, nil
	}
	text := strings.Join(env, "\x00") + "\x00\x00"
	units := utf16.Encode([]rune(text))
	if len(units) < 2 || units[len(units)-1] != 0 || units[len(units)-2] != 0 {
		return nil, errors.New("pty: environment block is not terminated")
	}
	return &units[0], nil
}
