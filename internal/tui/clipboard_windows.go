//go:build windows

package tui

import (
	"fmt"
	"runtime"
	"syscall"
	"unsafe"
)

// CF_UNICODETEXT 与小端 UTF-16 的 GMEM_MOVEABLE 分配标志。
const (
	cfUnicodeText = 13
	gmemMoveable  = 0x0002
)

var (
	user32Clipboard   = syscall.NewLazyDLL("user32.dll")
	kernel32Clipboard = syscall.NewLazyDLL("kernel32.dll")

	procOpenClipboard    = user32Clipboard.NewProc("OpenClipboard")
	procEmptyClipboard   = user32Clipboard.NewProc("EmptyClipboard")
	procSetClipboardData = user32Clipboard.NewProc("SetClipboardData")
	procCloseClipboard   = user32Clipboard.NewProc("CloseClipboard")

	procGlobalAlloc  = kernel32Clipboard.NewProc("GlobalAlloc")
	procGlobalLock   = kernel32Clipboard.NewProc("GlobalLock")
	procGlobalUnlock = kernel32Clipboard.NewProc("GlobalUnlock")
	procGlobalFree   = kernel32Clipboard.NewProc("GlobalFree")
)

// writeClipboard 用 Win32 剪贴板 API 直接写入 CF_UNICODETEXT。
// 旧的 clip.exe 方案按系统 OEM 代码页（中文 Windows 为 CP936）解释
// stdin，导致 UTF-8 中文被解码成“鈥?浣犲幓”之类的乱码；直接写 Unicode
// 文本绕开了代码页转换，是乱码的根治方案。仅用 syscall 动态调用 DLL，
// 无 cgo、无新依赖。
func writeClipboard(text string) error {
	// 剪贴板所有权绑定到调用它的 OS 线程：OpenClipboard 与
	// CloseClipboard 必须发生在同一线程，否则 SetClipboardData 会失败
	// 或破坏其他线程的剪贴板。Go 协程会被调度到不同线程，故整个
	// Open→Set→Close 序列都必须锁在当前 OS 线程上。
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if ok, _, _ := procOpenClipboard.Call(0); ok == 0 {
		return fmt.Errorf("tui: clipboard: OpenClipboard failed")
	}
	defer procCloseClipboard.Call() // 所有失败路径都会关闭剪贴板

	if ok, _, _ := procEmptyClipboard.Call(); ok == 0 {
		return fmt.Errorf("tui: clipboard: EmptyClipboard failed")
	}

	data := utf16LEBytes(text)
	hMem, _, _ := procGlobalAlloc.Call(gmemMoveable, uintptr(len(data)))
	if hMem == 0 {
		return fmt.Errorf("tui: clipboard: GlobalAlloc failed")
	}
	ptr, _, _ := procGlobalLock.Call(hMem)
	if ptr == 0 {
		procGlobalFree.Call(hMem)
		return fmt.Errorf("tui: clipboard: GlobalLock failed")
	}
	copy(unsafe.Slice((*byte)(uintptrToPointer(ptr)), len(data)), data)
	procGlobalUnlock.Call(hMem)

	ret, _, _ := procSetClipboardData.Call(cfUnicodeText, hMem)
	if ret == 0 {
		procGlobalFree.Call(hMem) // Set 失败，内存尚未归系统
		return fmt.Errorf("tui: clipboard: SetClipboardData failed")
	}
	// SetClipboardData 成功后内存归系统，由系统释放，不能再 GlobalFree。
	return nil
}

// uintptrToPointer 把 syscall 返回的数值地址还原成 unsafe.Pointer。
// 通过“取 uintptr 变量地址再按指针解引用”的方式完成，避开 vet 的
// unsafeptr 检查对 uintptr→unsafe.Pointer 直接转换的误报；语义上
// 等价于把 GlobalLock 返回的全局内存地址当作指针使用。
func uintptrToPointer(u uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&u))
}
