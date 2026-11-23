//go:build windows

package proc

import (
	"syscall"
	"unicode/utf16"
	"unsafe"
)

var (
	acpKernel32     = syscall.NewLazyDLL("kernel32.dll")
	acpProcGetACP   = acpKernel32.NewProc("GetACP")
	acpProcLeadByte = acpKernel32.NewProc("IsDBCSLeadByteEx")
	acpProcMBTW     = acpKernel32.NewProc("MultiByteToWideChar")
)

// hostACP 读取系统的 ANSI 代码页（GetACP），作为 UTF-8 判定失败后的回退编码。
func hostACP() uint32 {
	r, _, _ := acpProcGetACP.Call()
	return uint32(r)
}

// acpLeadByte 报告 b 是否为代码页 codepage 下的双字节字符前导字节。
func acpLeadByte(codepage uint32, b byte) bool {
	if codepage == 0 {
		return false
	}
	r, _, _ := acpProcLeadByte.Call(uintptr(codepage), uintptr(b))
	return r != 0
}

// decodeACPPrefix 计算 b 开头「完整 ACP 字符」的字节数，并用
// MultiByteToWideChar 把它们解码为 UTF-8 文本。末尾未凑成双字节的前导字节
// 在 eof=false 时保留（返回 consumed=0 表示整段待定），eof=true 时才一并消费。
// 解码调用失败时退回原始字节，避免无声丢数据。
func decodeACPPrefix(codepage uint32, b []byte, eof bool) (string, int) {
	if len(b) == 0 || codepage == 0 || codepage == cpUTF8 {
		return "", 0
	}
	n := 0
	for n < len(b) {
		if acpLeadByte(codepage, b[n]) {
			if n+1 < len(b) {
				n += 2
				continue
			}
			// 末尾前导字节：EOF 时按单字节消费，否则留给下一分块。
			if eof {
				n++
			}
			break
		}
		n++
	}
	if n == 0 {
		return "", 0
	}
	return decodeCodePage(codepage, b[:n]), n
}

// decodeCodePage 经 MultiByteToWideChar 把 codepage 编码的字节转成 UTF-8。
// 先用空宽字符缓冲区查询所需 UTF-16 单元数，再分配并实际转换。
func decodeCodePage(codepage uint32, b []byte) string {
	if len(b) == 0 {
		return ""
	}
	ptr := uintptr(unsafe.Pointer(&b[0]))
	needed, _, _ := acpProcMBTW.Call(uintptr(codepage), 0, ptr, uintptr(len(b)), 0, 0)
	if needed == 0 || needed > uintptr(1<<30) {
		return string(b) // 查询失败：退回原始字节，交由上层按替换字符呈现
	}
	wide := make([]uint16, int(needed))
	written, _, _ := acpProcMBTW.Call(uintptr(codepage), 0, ptr, uintptr(len(b)), uintptr(unsafe.Pointer(&wide[0])), needed)
	if written == 0 || written > needed {
		return string(b)
	}
	return string(utf16.Decode(wide[:int(written)]))
}
