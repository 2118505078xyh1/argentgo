package llm

import "strings"

// 本文件提供"不可信内容边界标记"原语：在把来自工具/外部的数据发给模型之前，
// 用一对固定边界把它包裹起来，并向模型表明其中的内容是不可信的（prompt
// injection 防护）。它是 llm 层的通用文本边界，与 agent 层的工具结果结构封装
// （agent.UntrustedResult）分属不同层，彼此独立。
//
// 这一切都是纯文本处理：没有调用方、不触碰任何 wire 协议、不改变任何请求行为。
// 仅依赖标准库。

const (
	// UntrustedOpen / UntrustedClose 是包裹不可信内容的固定边界标记。
	// 它们的值被测试锁定，改动会导致已标记文本的含义变化。
	UntrustedOpen  = "<untrusted_content>"
	UntrustedClose = "</untrusted_content>"

	// untrustedEscape 是用于转义边界标记的转义字符（反斜杠）。
	// content 中出现的边界标记会在其前面插入该字符，使伪造的边界无法逃逸；
	// 转义字符自身也会被转义，从而保证编码是单射、可无损还原。
	untrustedEscape = '\\'
)

// MarkUntrusted 用固定边界包裹 content，返回可直接发给模型的文本。
//
// content 中出现的 UntrustedOpen / UntrustedClose 会被转义为
// "\<untrusted_content>" / "\</untrusted_content>" 形式的可识别序列，
// 防止内容伪造边界形成嵌套注入。转义字符本身先被转义，因此任意 content
// 都能被 StripUntrusted 无损还原（round-trip）。函数只读入参。
func MarkUntrusted(content string) string {
	return UntrustedOpen + escapeUntrusted(content) + UntrustedClose
}

// ContainsUntrustedMarker 报告 s 中是否出现任一不可信边界标记（原文子串检测）。
//
// 注意：被转义的边界标记仍含有标记字面量，因此也会返回 true。本函数用于
// "是否存在标记文本"的快速判断；若需判断是否被真正包裹，应对其做
// StripUntrusted 往返比较。
func ContainsUntrustedMarker(s string) bool {
	return strings.Contains(s, UntrustedOpen) || strings.Contains(s, UntrustedClose)
}

// StripUntrusted 是 MarkUntrusted 的逆操作：尽力去掉首尾边界并对内部做反转义，
// 返回原始 content。
//
// "尽力而为"体现在：仅当 s 以 UntrustedOpen 开头时才去掉该前缀，仅当（去掉
// 前缀后的）s 以 UntrustedClose 结尾时才去掉该后缀；无论是否完整包裹，都会
// 对剩余文本做反转义。因此完整包裹的文本能精确还原，非包裹文本也不会报错。
func StripUntrusted(s string) string {
	inner := s
	if strings.HasPrefix(inner, UntrustedOpen) {
		inner = inner[len(UntrustedOpen):]
	}
	if strings.HasSuffix(inner, UntrustedClose) {
		inner = inner[:len(inner)-len(UntrustedClose)]
	}
	return unescapeUntrusted(inner)
}

// escapeUntrusted 对 content 中出现的转义字符与边界标记进行编码：
//   - 转义字符自身写成两份（"\\"），保证单射；
//   - 边界标记前插入一个转义字符，使其不再被当作真实边界。
//
// 按字节扫描是安全的：边界标记与转义字符都是 ASCII，而多字节 UTF-8 序列的
// 每个字节都 >= 0x80，不会被误判，CJK 等字符因此原样保留。
func escapeUntrusted(content string) string {
	var b strings.Builder
	b.Grow(len(content))
	for i := 0; i < len(content); {
		switch {
		case content[i] == untrustedEscape:
			b.WriteByte(untrustedEscape)
			b.WriteByte(untrustedEscape)
			i++
		case strings.HasPrefix(content[i:], UntrustedOpen):
			b.WriteByte(untrustedEscape)
			b.WriteString(UntrustedOpen)
			i += len(UntrustedOpen)
		case strings.HasPrefix(content[i:], UntrustedClose):
			b.WriteByte(untrustedEscape)
			b.WriteString(UntrustedClose)
			i += len(UntrustedClose)
		default:
			b.WriteByte(content[i])
			i++
		}
	}
	return b.String()
}

// unescapeUntrusted 反转义 escapeUntrusted 的编码：
//   - 转义字符 + 边界标记还原为标记本身；
//   - 两个连续转义字符还原为一个转义字符；
//   - 其余情况（含孤立的转义字符）原样输出，保持尽力而为的宽容性。
func unescapeUntrusted(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == untrustedEscape && i+1 < len(s) {
			next := s[i+1:]
			switch {
			case strings.HasPrefix(next, UntrustedOpen):
				b.WriteString(UntrustedOpen)
				i += 1 + len(UntrustedOpen)
				continue
			case strings.HasPrefix(next, UntrustedClose):
				b.WriteString(UntrustedClose)
				i += 1 + len(UntrustedClose)
				continue
			case next[0] == untrustedEscape:
				b.WriteByte(untrustedEscape)
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
