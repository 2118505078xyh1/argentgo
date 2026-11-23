//go:build windows

package pty

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

// ConPTY 从 Windows 10 1809（build 17763）起可用。这里的一切都直接走 kernel32：无 cgo、构建时无 gcc。callResizePseudoConsole 和 callClosePseudoConsole 是 startPlatform 之后唯一触碰 HPCON 的地方。它们是间接层，使 HPCON 生命周期回归测试无需真实控制台即可观察其顺序；生产行为不变。
// 一切都直接走 kernel32：无 cgo、构建时无 gcc。
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

// callResizePseudoConsole 和 callClosePseudoConsole 是唯一
// 在 startPlatform 之后触碰 HPCON 的地方。它们是间接层，使 HPCON 生命周期
// 回归测试无需真实控制台即可观察其顺序；生产
// 行为不变。
var (
	callResizePseudoConsole = func(console syscall.Handle, window uintptr) error {
		r, _, callErr := procResizePseudoConsole.Call(uintptr(console), window)
		if failed(r) {
			return fmt.Errorf("pty: ResizePseudoConsole: %w", callErr)
		}
		return nil
	}
	callClosePseudoConsole = func(console syscall.Handle) {
		procClosePseudoConsole.Call(uintptr(console))
	}
)

// coord 即 COORD：Win32 ABI 按值传递的 4 字节结构体，因此它作为一个打包整数参数而非指针传递。failed 报告失败的 HRESULT。conPty 拥有一个 HPCON 及其附加的子进程。控制台复制了这些；我们的副本必须关闭，否则子进程永不看到 EOF。控制台提供 stdio；请求显式句柄（即使是 null）会使 CRT 打开继承的控制台而非伪控制台。
// 作为一个打包整数参数而非指针传递。
func coordOf(size Size) uintptr {
	return uintptr(uint32(uint16(size.Cols)) | uint32(uint16(size.Rows))<<16)
}

// failed 报告失败的 HRESULT。
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

// conPty 拥有一个 HPCON 及其附加的子进程。
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
	// 控制台复制了这些；我们的副本必须关闭，否则子进程永不看到 EOF。
	syscall.CloseHandle(inRead)
	syscall.CloseHandle(outWrite)

	attrs, err := newAttributeList(console)
	if err != nil {
		procClosePseudoConsole.Call(uintptr(console))
		syscall.CloseHandle(inWrite)
		syscall.CloseHandle(outRead)
		return nil, err
	}
	defer attrs.delete()

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
	// 与 unix 路径一致地复用 proc.SanitizeEnv：去掉宿主私有变量后
	// 再追加显式条目，避免嵌套 agent 误继承父会话状态。
	if env := sanitizedEnv(cmd.Env); len(env) > 0 {
		envPtr, err = environmentBlock(env)
		if err != nil {
			procClosePseudoConsole.Call(uintptr(console))
			syscall.CloseHandle(inWrite)
			syscall.CloseHandle(outRead)
			return nil, err
		}
		flags |= createUnicodeEnvironment
	}

	startup := startupInfoEx{attributeList: attrs.list}
	startup.startupInfo.cb = uint32(unsafe.Sizeof(startup))
	// 控制台提供 stdio；请求显式句柄（即使是 null）会使
	// CRT 打开继承的控制台而非伪控制台。
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

// attributeList 把 Win32 列表指针与其背后的 Go 分配配对。
// uintptr 不是 Go 引用：如果缓冲区只能通过这些数值
// 列表地址到达，GC 可能在 newAttributeList 与 CreateProcessW 之间
// 回收它，给 Windows 留下悬垂指针。
type attributeList struct {
	list   uintptr
	buffer []byte
}

func newAttributeList(console syscall.Handle) (*attributeList, error) {
	var size uintptr
	// 第一次调用只报告所需大小。
	procInitializeProcThreadAttributeList.Call(0, 1, 0, uintptr(unsafe.Pointer(&size)))
	if size == 0 {
		return nil, errors.New("pty: could not size the attribute list")
	}
	buffer := make([]byte, size)
	list := uintptr(unsafe.Pointer(&buffer[0]))
	if r, _, callErr := procInitializeProcThreadAttributeList.Call(
		list, 1, 0, uintptr(unsafe.Pointer(&size)),
	); r == 0 {
		return nil, fmt.Errorf("pty: InitializeProcThreadAttributeList: %w", callErr)
	}
	attrs := &attributeList{list: list, buffer: buffer}
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
		attrs.delete()
		return nil, fmt.Errorf("pty: UpdateProcThreadAttribute: %w", callErr)
	}
	return attrs, nil
}

// delete 在没有进程能再用它时释放 Win32 列表。显式
// attributeList 把 Win32 列表指针与其背后的 Go 分配配对。uintptr 不是 Go 引用：如果缓冲区只能通过这些数值列表地址到达，GC 可能在 newAttributeList 与 CreateProcessW 之间回收它，给 Windows 留下悬垂指针。第一次调用只报告所需大小。delete 在没有进程能再用它时释放 Win32 列表。显式 KeepAlive 把 Go 缓冲区锚定到 Win32 调用返回为止。
func (a *attributeList) delete() {
	procDeleteProcThreadAttributeList.Call(a.list)
	runtime.KeepAlive(a.buffer)
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
	// 整个平台调用都持有 c.mu。读完 c.console 后释放它是错误的，
	// closeConsole 至多释放一次 HPCON。从 Wait（子进程退出时）和 Close（显式拆卸时）调用都是安全的，顺序任意。阻塞直到 conhost 消失；子进程退出后这很快。
	// 使 Resize 操作已释放的句柄 —— 与进程句柄被防范的关闭后使用相同，
	// 只是发生在控制台句柄上。
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.console == 0 {
		return errors.New("pty: terminal is closed")
	}
	return callResizePseudoConsole(c.console, coordOf(size))
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
	// 锁一释放，Close 就可能关闭原始进程句柄。
	// 整个等待使用私有副本，使并发的
	// Close+Wait 不会让 GetExitCodeProcess 以 "无效句柄" 失败并
	// 把普通退出变成虚假的 Wait 错误。
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
	// 只要 HPCON 存活，ConPTY 就保持输出管道打开，即使
	// 子进程已消失。在这里释放它，使排空输出的读者
	// 观察到 EOF 而非阻塞直到有人调用 Close。
	// 缓冲字节仍可读：关闭控制台只关闭管道的
	// 控制台端，不丢弃排队输出。
	c.closeConsole()
	return int(code), nil
}

// closeConsole 至多释放一次 HPCON。从 Wait 和 Close 调用都是安全的，
// 顺序任意。
func (c *conPty) closeConsole() {
	c.mu.Lock()
	console := c.console
	c.console = 0
	c.mu.Unlock()
	if console != 0 {
		// 阻塞直到 conhost 消失；子进程退出后这很快。
		callClosePseudoConsole(console)
	}
}

// duplicateProcess 为活的子进程状态查询返回私有句柄。对 c.process 的读取
// 和复制都在 c.mu 下进行，使并发 Close 无法在两者之间关闭原始句柄并使
// 调用方持有陈旧数值 —— 与 Wait 用自己的副本防范的危险相同。
// 当缓存状态已能回答查询（子进程被回收、Close 已开始拆卸终端，或句柄已
// 自己的副本。当缓存状态已能回答查询（
// 消失）时 ok 为 false，此时调用方不得使用返回的句柄。
// 在两把锁之间子进程被回收或拆卸；已没有可等待的活进程。
func (c *conPty) duplicateProcess() (owned syscall.Handle, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.reaped || c.closed || c.process == 0 {
		return 0, false
	}
	self, err := syscall.GetCurrentProcess()
	if err != nil {
		return 0, false
	}
	if err := syscall.DuplicateHandle(self, c.process, self, &owned, 0, false, syscall.DUPLICATE_SAME_ACCESS); err != nil {
		return 0, false
	}
	return owned, true
}

func (c *conPty) Exited() bool {
	c.mu.Lock()
	reaped := c.reaped
	process := c.process
	closed := c.closed
	c.mu.Unlock()
	if reaped || process == 0 || closed {
		return true
	}
	owned, ok := c.duplicateProcess()
	if !ok {
		// 在两把锁之间子进程被回收或拆卸；已没有
		// 在这里回收使之后的 Wait 仍有退出码可报告。
		return true
	}
	defer syscall.CloseHandle(owned)
	r, _, _ := procWaitForSingleObject.Call(uintptr(owned), 0)
	return r == waitObject0
}

func (c *conPty) ExitCode() int {
	c.mu.Lock()
	reaped := c.reaped
	code := c.exitCode
	process := c.process
	closed := c.closed
	c.mu.Unlock()
	if reaped {
		return code
	}
	if process == 0 || closed {
		return -1
	}
	owned, ok := c.duplicateProcess()
	if !ok {
		return -1
	}
	defer syscall.CloseHandle(owned)
	if r, _, _ := procWaitForSingleObject.Call(uintptr(owned), 0); r != waitObject0 {
		return -1
	}
	var value uint32
	if r, _, _ := procGetExitCodeProcess.Call(uintptr(owned), uintptr(unsafe.Pointer(&value))); r == 0 {
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
	reaped := c.reaped
	c.mu.Unlock()

	if process != 0 {
		// 在与 Wait 用于复制的同一把锁下发布已关闭句柄。
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
	c.closeConsole()
	if process != 0 {
		// 否则 Wait 可能在这里 CloseHandle 与下面清除 c.process 之间的窗口
		// 观察到仍非零的句柄。
		c.mu.Lock()
		c.process = 0
		syscall.CloseHandle(process)
		c.mu.Unlock()
	}
	return nil
}

// resolve 把裸程序名转换为 CreateProcessW 可信任的路径。
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

// buildCommandLine 把 argv 渲染为 CreateProcessW 解析的单一字符串，
// 按 MSVC 规则加引号，Go 或 Rust 子进程会以同样方式解除。
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

// resolve 把裸程序名转换为 CreateProcessW 可信任的路径。buildCommandLine 把 argv 渲染为 CreateProcessW 解析的单一字符串，按 MSVC 规则加引号，Go 或 Rust 子进程会以同样方式解除。environmentBlock 构建 CreateProcessW 使用的双 NUL 结尾 UTF-16 块。
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
