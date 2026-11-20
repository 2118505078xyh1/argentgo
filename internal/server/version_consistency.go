package server

import "strings"

// versionsConsistent 比较发布 tag 与二进制报告的版本号是否一致。
// 两者都会先经 normalizeVersion 规范化（去首尾空白、统一小写、去掉
// 一个前导 v），任一输入非法或为空时返回 false。该函数只做纯字符串
// 处理、不执行任何 I/O，供构建/测试期的发布一致性校验辅助使用。
func versionsConsistent(tag, binaryVersion string) bool {
	normalizedTag, ok := normalizeVersion(tag)
	if !ok {
		return false
	}
	normalizedBinary, ok := normalizeVersion(binaryVersion)
	if !ok {
		return false
	}
	return normalizedTag == normalizedBinary
}

// normalizeVersion 把版本字符串规范化为可比较形式，并在内容不是合法
// 版本号时以 ok=false 拒绝。规范化顺序：去首尾空白、统一小写、去掉
// 一个前导 v。合法版本号只允许 ASCII 数字、小写字母以及 .、-、+，
// 因此 CJK 等非 ASCII 字符会被判为非法；同时 strings 包基于 rune
// 的处理保证多字节字符在规范化过程中不会被破坏（CJK 安全）。
func normalizeVersion(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if strings.HasPrefix(s, "v") {
		s = s[1:]
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r == '.', r == '-', r == '+':
			continue
		default:
			return "", false
		}
	}
	return s, true
}
