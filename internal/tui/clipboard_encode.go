package tui

import "unicode/utf16"

// utf16LEBytes 把字符串编码为小端 UTF-16，结尾补一个 UTF-16 NUL，
// 不加 BOM（clip.exe 会把 BOM 当作正文数据）。Windows 的
// CF_UNICODETEXT 要求这种布局。拆成跨平台纯函数是為了能在
// 非 Windows 平台上对编码做单测，无需依赖真实系统剪贴板。
func utf16LEBytes(s string) []byte {
	units := utf16.Encode([]rune(s))
	out := make([]byte, 0, (len(units)+1)*2)
	for _, u := range units {
		out = append(out, byte(u), byte(u>>8)) // 小端：低字节在前
	}
	return append(out, 0, 0) // NUL 终结符（两个零字节）
}
