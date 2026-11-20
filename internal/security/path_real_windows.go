//go:build windows

package security

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"
)

var (
	finalPathKernel32 = syscall.NewLazyDLL("kernel32.dll")
	finalPathProc     = finalPathKernel32.NewProc("GetFinalPathNameByHandleW")
)

// FILE_NAME_NORMALIZED | VOLUME_NAME_DOS。DOS 形式以 \\?\C:\... 返回，
// 下面会去掉前缀，使其能与字面路径干净地比较。
const finalPathNameFlags = 0x00000000

// realPathExisting 向内核询问一个已打开的句柄实际位于何处。os.Open 会跟随
// 目录 junction（它不传 FILE_FLAG_OPEN_REPARSE_POINT），而 Windows 上的
// filepath.EvalSymlinks 不处理 junction，因此这是唯一可靠的纯标准库解析方式。
func realPathExisting(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	resolved, err := finalPathNameByHandle(syscall.Handle(file.Fd()))
	if err != nil {
		// 针对内核调用不可用的罕见情况的兜底：
		// 绝不让未解析的重解析点通过。
		if hasReparsePoint(path) {
			return "", fmt.Errorf("security: reparse point %q could not be resolved on Windows: %w", path, err)
		}
		return "", err
	}
	if hasReparsePoint(resolved) {
		return "", fmt.Errorf("security: resolved path %q is still a reparse point; refusing", resolved)
	}
	return resolved, nil
}

func finalPathNameByHandle(handle syscall.Handle) (string, error) {
	if err := finalPathProc.Find(); err != nil {
		return "", fmt.Errorf("security: GetFinalPathNameByHandleW unavailable: %w", err)
	}
	// 零大小的缓冲区使 API 报告所需的 WCHAR 长度（包括结尾的 NUL）。
	// 预期会返回非零值；此时最后的错误是 ERROR_INSUFFICIENT_BUFFER，会被忽略。
	size, _, _ := finalPathProc.Call(uintptr(handle), 0, 0, finalPathNameFlags)
	if size == 0 {
		return "", fmt.Errorf("security: GetFinalPathNameByHandleW returned no length")
	}
	buf := make([]uint16, size+1)
	n, _, callErr := finalPathProc.Call(
		uintptr(handle), uintptr(unsafe.Pointer(&buf[0])), size, finalPathNameFlags)
	if n == 0 {
		if callErr != nil {
			return "", callErr
		}
		return "", fmt.Errorf("security: GetFinalPathNameByHandleW failed")
	}
	return trimExtendedPath(syscall.UTF16ToString(buf)), nil
}

func trimExtendedPath(path string) string {
	if strings.HasPrefix(path, `\\?\UNC\`) {
		return `\\` + path[len(`\\?\UNC\`):]
	}
	if strings.HasPrefix(path, `\\?\`) {
		return path[len(`\\?\`):]
	}
	return path
}

// hasReparsePoint 报告 path 的最后一段组件是否是符号链接或 Windows 重解析点
// （junction、挂载点、重复数据删除链接）。它是失败即拒绝的兜底手段，
// 而不是主要的解析机制。
func hasReparsePoint(path string) bool {
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	return isLinkInfo(info)
}

// isLinkInfo 报告 info 是否描述符号链接或任何其他 Windows 重解析点
// （junction、挂载点、重复数据删除链接）。它与 linkTarget 共享，
// 使悬空 junction 被本包中与其他重解析点检查相同的规则识别。
func isLinkInfo(info os.FileInfo) bool {
	if info == nil {
		return false
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return true
	}
	if data, ok := info.Sys().(*syscall.Win32FileAttributeData); ok {
		return data.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
	}
	return false
}

// normalizeLinkTarget 去掉 Windows 上 os.Readlink 可能返回的 NT（\??\）和
// 扩展长度（\\?\）前缀，使目标能与字面路径干净地比较。若不去掉 NT 形式，
// filepath.IsAbs 会把目标当作相对路径并把它拼到链接所在目录下，从而隐藏
// 一次真实的逃逸。
func normalizeLinkTarget(path string) string {
	if strings.HasPrefix(path, `\??\UNC\`) {
		return `\\` + path[len(`\??\UNC\`):]
	}
	if strings.HasPrefix(path, `\??\`) {
		return path[len(`\??\`):]
	}
	return trimExtendedPath(path)
}
