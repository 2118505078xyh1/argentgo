//go:build !windows

package proc

// hostACP 在非 Windows 平台返回 0，表示没有可用的 ANSI 代码页回退。
func hostACP() uint32 { return 0 }

// acpLeadByte 在非 Windows 平台恒为 false。
func acpLeadByte(codepage uint32, b byte) bool { return false }

// decodeACPPrefix 在非 Windows 平台不解码，返回零消费量。
func decodeACPPrefix(codepage uint32, b []byte, eof bool) (string, int) { return "", 0 }
