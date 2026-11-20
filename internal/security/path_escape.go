package security

import "strings"

// looksLikePathEscape 是一个纯词法判定：报告 path 是否采用了看起来要逃出
// 工作区的写法。它不触碰文件系统，也不解析符号链接；真实位置的解析由
// InWriteRoots / ResolveRealPath 负责。这里覆盖的是"通向逃逸的写法"，
// 包括链接目标经常被藏入的 .. 遍历与绝对路径。
//
// 识别的形态：
//   - .. 段，且 / 与 \ 都当作段分隔符；
//   - 绝对路径：Unix 的 /、Windows 的根目录 \，以及盘符 X:；
//   - UNC：\\server\share、//server/share 及两者混合的开头；
//   - URL 百分号编码（含多重编码）的 .、/、\ 与 NUL，如 %2e%2e；
//   - 过宽 UTF-8 编码，如用 %C0%AE 表示一个 '.'；
//   - NUL 字节，包括 %00 解码后的结果。
//
// 它刻意不把单独的 . 段、点开头的普通文件名、名字里含 .. 的普通文件名
// 或 CJK 文件名当作逃逸。
func looksLikePathEscape(path string) bool {
	normalized := normalizeEscapeCandidate(strings.TrimSpace(path))
	if strings.IndexByte(normalized, 0) >= 0 {
		return true
	}
	// 开头是分隔符即逃逸：Unix 绝对路径、Windows 根目录路径，以及
	// 前两个字符都是分隔符的 UNC 路径（\\server\share、//server/share）。
	if len(normalized) > 0 && isPathSeparator(normalized[0]) {
		return true
	}
	// Windows 盘符写法，包括 C:foo 这种盘符相对路径，也可能落到工作区外。
	if len(normalized) >= 2 && isASCIILetter(normalized[0]) && normalized[1] == ':' {
		return true
	}
	return hasParentSegment(normalized)
}

// hasParentSegment 逐段检查是否存在恰好由两个点组成的段。按字节扫描，
// 因此 CJK 文件名里的多字节序列和名字中夹带的 .. 都不会被误判。
func hasParentSegment(s string) bool {
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || isPathSeparator(s[i]) {
			if i-start == 2 && s[start] == '.' && s[start+1] == '.' {
				return true
			}
			start = i + 1
		}
	}
	return false
}

// maxEscapeDecodeRounds 限制百分号解码的层数，既覆盖常见的双重编码，
// 也保证恶意输入不会让解码循环无休止地运行。
const maxEscapeDecodeRounds = 4

// normalizeEscapeCandidate 反复剥离百分号编码与过宽 UTF-8 编码，
// 让后续检查只需面对字面的分隔符、点和 NUL。
func normalizeEscapeCandidate(path string) string {
	current := path
	for range maxEscapeDecodeRounds {
		next := collapseOverlongASCII(decodePercentEscapes(current))
		if next == current {
			break
		}
		current = next
	}
	return current
}

// decodePercentEscapes 把 %XX 还原为对应字节；非法或不完整的转义
// 原样保留，避免把普通文件名里的 '%' 当成转义。返回值可能不是合法的
// UTF-8，但后续只按字节检查分隔符、点、盘符与 NUL，因此是安全的。
func decodePercentEscapes(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); {
		if s[i] == '%' && i+2 < len(s) {
			hi, lo := hexNibble(s[i+1]), hexNibble(s[i+2])
			if hi >= 0 && lo >= 0 {
				out = append(out, byte(hi)<<4|byte(lo))
				i += 3
				continue
			}
		}
		out = append(out, s[i])
		i++
	}
	return string(out)
}

func hexNibble(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

// collapseOverlongASCII 把用多字节 UTF-8 过宽表示的 ASCII 字符压回单字节，
// 例如 C0 AE 还原为 '.'。这类写法常与百分号编码叠加，用来绕过只匹配
// 字面 ".." 的检查。合法 UTF-8（包括 CJK）不会命中这里的条件。
func collapseOverlongASCII(s string) string {
	found := false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case 0xC0, 0xC1, 0xE0, 0xF0:
			found = true
		}
		if found {
			break
		}
	}
	if !found {
		return s
	}
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); {
		if value, width, ok := overlongASCII(s, i); ok {
			out = append(out, value)
			i += width
			continue
		}
		out = append(out, s[i])
		i++
	}
	return string(out)
}

// overlongASCII 识别从 s[i] 开始、用多字节序列过宽编码一个 ASCII 字符的
// 写法，返回还原后的字节与消耗的字节数；不是过宽编码时 ok 为 false。
func overlongASCII(s string, i int) (byte, int, bool) {
	b0 := s[i]
	switch {
	case (b0 == 0xC0 || b0 == 0xC1) && i+1 < len(s):
		b1 := s[i+1]
		if b1&0xC0 != 0x80 {
			return 0, 0, false
		}
		value := int(b0&0x1F)<<6 | int(b1&0x3F)
		if value < 0x80 {
			return byte(value), 2, true
		}
	case b0 == 0xE0 && i+2 < len(s):
		b1, b2 := s[i+1], s[i+2]
		if b1&0xC0 != 0x80 || b2&0xC0 != 0x80 {
			return 0, 0, false
		}
		value := int(b0&0x0F)<<12 | int(b1&0x3F)<<6 | int(b2&0x3F)
		if value < 0x80 {
			return byte(value), 3, true
		}
	case b0 == 0xF0 && i+3 < len(s):
		b1, b2, b3 := s[i+1], s[i+2], s[i+3]
		if b1&0xC0 != 0x80 || b2&0xC0 != 0x80 || b3&0xC0 != 0x80 {
			return 0, 0, false
		}
		value := int(b0&0x07)<<18 | int(b1&0x3F)<<12 | int(b2&0x3F)<<6 | int(b3&0x3F)
		if value < 0x80 {
			return byte(value), 4, true
		}
	}
	return 0, 0, false
}

func isPathSeparator(c byte) bool { return c == '/' || c == '\\' }
