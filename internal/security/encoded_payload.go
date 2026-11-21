package security

import "encoding/base64"

// 编码载荷的检测阈值。base64 要求足够长的串，转义要求连续多个，以免把
// 单个 \x41、\u4e2d 之类的合法字面量误判为载荷。
const (
	minBase64RunLen  = 16   // 视为“长串”的最小 base64 字符数
	maxBase64RunLen  = 4096 // 单次解码的上限，保证分配与输入长度无关
	minDecodedLen    = 8    // 解码后至少要有这么多字节才纳入判定
	minHexEscapeRun  = 3    // 连续 \xHH 的最小个数
	minUnicodeEscRun = 2    // 连续 \uHHHH / \UHHHHHHHH 的最小个数
)

// looksEncodedPayload 报告 s 中是否含有疑似隐藏命令的编码载荷：长 base64 串、
// 连续的 \x 十六进制转义，或连续的 Unicode 转义（\uHHHH / \UHHHHHHHH）。
// 它只做词法判定，不解码执行，也不读写任何外部状态。
//
// 语义与实现要点：
//   - CJK 安全：按字节扫描只识别 ASCII 反斜杠与标准 base64 字符；UTF-8 中的
//     中日韩多字节序列不会被切开，字面量中文（如“报告”）不会命中。显式写成
//     转义形式的中文（如 \xe4\xb8\xad）属于编码形态，刻意会被检测。
//   - 有界：单趟线性扫描，base64 只检查长度落在阈值内的串，超长串仅取固定长度
//     的前缀解码，因此分配上界是 maxBase64RunLen；无递归、无状态。
//   - 稳定：纯函数，无 map 遍历与随机数，同一输入永远得到同一结果。
func looksEncodedPayload(s string) bool {
	return findEncodedPayload(s) != ""
}

// findEncodedPayload 返回 s 中最先出现的疑似编码载荷（s 的原文子串），没有则
// 返回空串。返回子串而不是布尔值，令命令守卫可以把命中片段当作违规 token 报告。
func findEncodedPayload(s string) string {
	for i := 0; i < len(s); {
		if s[i] == '\\' {
			if n, end := countHexEscapes(s, i); n >= minHexEscapeRun {
				return s[i:end]
			}
			if n, end := countUnicodeEscapes(s, i); n >= minUnicodeEscRun {
				return s[i:end]
			}
			i++
			continue
		}
		if isBase64Byte(s[i]) {
			start := i
			for i < len(s) && isBase64Byte(s[i]) {
				i++
			}
			if candidate := base64Candidate(s[start:i]); candidate != "" {
				return candidate
			}
			continue
		}
		i++
	}
	return ""
}

// countHexEscapes 统计从 s[start] 开始连续的 \xHH（大小写均可）转义个数，
// 并返回这段连续转义的结束位置。
func countHexEscapes(s string, start int) (count, end int) {
	i := start
	for i+3 < len(s) {
		if s[i] != '\\' || (s[i+1] != 'x' && s[i+1] != 'X') ||
			!isHexDigit(s[i+2]) || !isHexDigit(s[i+3]) {
			break
		}
		count++
		i += 4
	}
	return count, i
}

// countUnicodeEscapes 统计从 s[start] 开始连续的 \uHHHH / \UHHHHHHHH 转义个数，
// 并返回这段连续转义的结束位置。
func countUnicodeEscapes(s string, start int) (count, end int) {
	i := start
	for i < len(s) {
		width := unicodeEscapeWidth(s, i)
		if width == 0 {
			break
		}
		count++
		i += width
	}
	return count, i
}

// unicodeEscapeWidth 返回 s[i] 处 Unicode 转义的宽度（6 或 10 字节）；不是
// 合法的完整转义则返回 0。
func unicodeEscapeWidth(s string, i int) int {
	if i >= len(s) || s[i] != '\\' || i+1 >= len(s) {
		return 0
	}
	switch s[i+1] {
	case 'u':
		if i+6 <= len(s) && isHexRun(s[i+2:i+6]) {
			return 6
		}
	case 'U':
		if i+10 <= len(s) && isHexRun(s[i+2:i+10]) {
			return 10
		}
	}
	return 0
}

// base64Candidate 检查一段由 base64 字符组成的极大串。只有长度达标、能解码且
// 解码结果是可打印文本时才返回证据片段（必要时截断以限制分配），否则返回空串。
func base64Candidate(run string) string {
	if len(run) < minBase64RunLen {
		return ""
	}
	length := len(run)
	if length > maxBase64RunLen {
		length = maxBase64RunLen
	}
	candidate := run[:length]
	// '=' 只能作为末尾填充出现，且最多两个；中间的 '=' 说明这不是规范 base64。
	pad := 0
	for pad < len(candidate) && candidate[len(candidate)-1-pad] == '=' {
		pad++
	}
	if pad > 2 {
		return ""
	}
	core := candidate[:len(candidate)-pad]
	if len(core)%4 == 1 {
		return ""
	}
	for i := 0; i < len(core); i++ {
		if core[i] == '=' {
			return ""
		}
	}
	decoded, err := base64.RawStdEncoding.DecodeString(core)
	if err != nil || len(decoded) < minDecodedLen {
		return ""
	}
	printable := 0
	for _, c := range decoded {
		if c >= 0x20 && c <= 0x7e {
			printable++
		}
	}
	// 编码后的命令解码为可打印 ASCII；长单词之类的 base64 字母串解码为高位
	// 字节与控制字符，借此把二者区分开。
	if printable*10 < len(decoded)*9 {
		return ""
	}
	return candidate
}

func isBase64Byte(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	return c == '+' || c == '/' || c == '='
}

func isHexDigit(c byte) bool {
	switch {
	case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		return true
	}
	return false
}

func isHexRun(s string) bool {
	for i := 0; i < len(s); i++ {
		if !isHexDigit(s[i]) {
			return false
		}
	}
	return true
}
