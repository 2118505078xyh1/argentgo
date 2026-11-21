package llm

import "strings"

// 本文件在 untrusted_boundary.go 的单射转义原语之上提供三个"工具结果边界"
// 辅助函数，供上层（app 层）在把模型可见的工具结果文本加固时选用。
//
// 设计约束与 untrusted_boundary.go 一致：纯文本处理、只读入参、只依赖标准库、
// 没有调用方、不触碰任何 wire 协议、不改变任何请求行为。三个函数均为 opt-in：
// 只有显式开启才会改变文本，关闭时逐字节原样返回。

// MarkToolBoundary 按 enabled 决定是否用不可信边界包裹 text。
//
// enabled 为 false 时原样返回 text（逐字节不变，不做任何转义或裁剪）。
// enabled 为 true 时等价于 MarkUntrusted(text)：外层加上一对真实边界，
// 并把 text 中已经存在的边界标记（含转义字符）交给 MarkUntrusted 的单射转义
// 处理，使伪造/已有的边界变为转义序列，不会形成第二个真实边界，从而避免
// 双重包裹歧义。因此 text 可以是普通文本，也可以是已经被 MarkUntrusted
// 包裹过的文本——后者会被再包一层，但只有最外层是真实边界。
//
// 函数只读入参；边界标记与转义字符均为 ASCII，按字节扫描不会破坏 CJK 等
// 多字节 UTF-8 序列。
func MarkToolBoundary(text string, enabled bool) string {
	if !enabled {
		return text
	}
	return MarkUntrusted(text)
}

// BoundaryStats 统计 s 中"未被转义"的开/闭边界标记数量。
//
// 扫描规则与 unescapeUntrusted 的解码规则保持一致：转义字符 + 边界标记
// （即 \<untrusted_content> / \</untrusted_content>）计为转义序列，不计数；
// 两个连续转义字符（\\）计为一个字面转义字符，也不计数；只有直接出现、
// 没有被转义的边界标记才计入 open / closed。
//
// 该函数用于验证防注入：完整包裹一段普通文本后，open 与 closed 应各为 1；
// 即使用内容里伪造了边界，它们也会被转义而不被计入。
func BoundaryStats(s string) (open, closed int) {
	for i := 0; i < len(s); {
		if s[i] == untrustedEscape && i+1 < len(s) {
			next := s[i+1:]
			switch {
			case strings.HasPrefix(next, UntrustedOpen):
				// 转义的开标记：连同转义字符一起跳过，不计数。
				i += 1 + len(UntrustedOpen)
				continue
			case strings.HasPrefix(next, UntrustedClose):
				// 转义的闭标记：连同转义字符一起跳过，不计数。
				i += 1 + len(UntrustedClose)
				continue
			case next[0] == untrustedEscape:
				// 转义字符自身：跳过两个字节，不计数。
				i += 2
				continue
			}
		}
		if strings.HasPrefix(s[i:], UntrustedOpen) {
			open++
			i += len(UntrustedOpen)
			continue
		}
		if strings.HasPrefix(s[i:], UntrustedClose) {
			closed++
			i += len(UntrustedClose)
			continue
		}
		i++
	}
	return open, closed
}

// StripAllBoundaries 是 MarkToolBoundary / MarkUntrusted 的尽力剥离辅助。
//
// 与 StripUntrusted 只去掉首尾一对边界不同，本函数扫描并剥离 s 中出现的
// 所有未转义边界标记，同时把转义序列反转义回字面量（\<...> 还原为标记本身，
// \\ 还原为单个转义字符）。扫描顺序保证先识别转义、再识别真实边界，因此
// 被转义的标记会作为普通内容保留，不会被当作边界误删。
//
// 因此 StripAllBoundaries(MarkToolBoundary(text, true)) == text，即内容里
// 伪造的边界也会被还原成原文，适合做 round-trip 校验。对非完整包裹或已损坏
// 的输入同样宽容：能剥多少剥多少，不报错、不 panic。
func StripAllBoundaries(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == untrustedEscape && i+1 < len(s) {
			next := s[i+1:]
			switch {
			case strings.HasPrefix(next, UntrustedOpen):
				// 转义的开标记还原为字面标记并写出，不当作边界剥离。
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
		if strings.HasPrefix(s[i:], UntrustedOpen) {
			// 真实边界：跳过（剥离）。
			i += len(UntrustedOpen)
			continue
		}
		if strings.HasPrefix(s[i:], UntrustedClose) {
			i += len(UntrustedClose)
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
