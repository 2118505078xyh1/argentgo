package update

import "strings"

// checksumLineMatches 解析一行规范的 sha256sum 校验和记录，格式为
// "<64 位十六进制摘要>  <文件名>"（摘要与文件名之间恰好两个空格）。
// 仅当整行格式合法、摘要为 64 位十六进制数，且文件名与 wantName
// 逐字节精确相等时，返回规范化为小写的摘要和 ok=true；任何格式错误、
// 非十六进制字符、长度不符或文件名不匹配都返回 ok=false。
//
// 摘要部分固定为 64 个 ASCII 字节，按字节切片不会切断多字节字符；
// 文件名部分只做整体相等比较，不解码、不裁剪、不重新编码，
// 因此对 CJK 及其它 UTF-8 文件名安全。对相同输入总是返回相同结果。
func checksumLineMatches(line string, wantName string) (hash string, ok bool) {
	// SHA-256 摘要为 32 字节，十六进制表示为 64 个字符。
	const digestLen = 64
	const separator = "  "

	// 至少需要：摘要 + 两个空格分隔符 + 一个文件名字节。
	if len(line) < digestLen+len(separator)+1 {
		return "", false
	}
	if line[digestLen:digestLen+len(separator)] != separator {
		return "", false
	}

	digest := line[:digestLen]
	for i := 0; i < len(digest); i++ {
		c := digest[i]
		if !((c >= '0' && c <= '9') ||
			(c >= 'a' && c <= 'f') ||
			(c >= 'A' && c <= 'F')) {
			return "", false
		}
	}

	name := line[digestLen+len(separator):]
	if name != wantName {
		return "", false
	}
	return strings.ToLower(digest), true
}
