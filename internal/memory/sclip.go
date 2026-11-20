package memory

// 按字簇安全裁剪，仅用标准库实现。
//
// 按字簇安全裁剪，仅用标准库实现。
// "字簇"是人类读作一个字符的单位。在任意字节或 rune 边界处
// 拆分字符串会破坏组合序列（"e" + U+0301）、ZWJ emoji（👨‍👩‍👧‍👦）、
// 区域指示符旗帜（🇨🇳）或韩文 jamo 序列，产生无效或无意义的
// 从不在这种字簇内部切割。
//
// 覆盖范围（务实、仅标准库）：
//   - 基础 rune 加其后的 Unicode 组合标记（Mn/Mc/Me）、
//     变体选择符（FE00–FE0F）、ZWNJ、emoji 肤色修饰符和
//     emoji 标签字符；
//   - ZWJ（U+200D）链：ZWJ 之后的 rune 被粘入该字簇；
//   - 成对的区域指示符（U+1F1E6–U+1F1FF）组成单个旗帜字簇；
//   - 韩文 jamo 序列按 L/V/T 组合规则分组；
//   - CRLF 保持在一起。
//
// 诚实的局限：这不是完整的 UAX #29 实现。它没有
// extended-pictographic / prepend / spacing-mark / Indic virama 规则，不
// 实现完整的 GB9c（印度语系连字）或 GB11（扩展图形 ZWJ）
// 门控，也不能完美地对中间带扩展符的区域指示符分词。
// 它刻意保守：有疑问时就把字节
// 保持在一起，因此对 exotic 文字可能少数字簇，但绝不会输出
// 悬空的组合标记或半个 emoji。需要完全符合标准的调用方
// 应使用专门的 UAX #29 实现；标准库中没有。

import (
	"unicode"
	"unicode/utf8"
)

const (
	zwj      = 0x200D  // zero-width joiner: glues the next rune into a cluster
	riStart  = 0x1F1E6 // regional indicator "A"
	riEnd    = 0x1F1FF // regional indicator "Z"
	ellipsis = "…"
)

// ClipGraphemes 返回 s 的前 maxClusters 个字簇。
// 结果始终是有效的 UTF-8 字符串，且永不长于 s。非正数
// maxClusters 返回空字符串。
func ClipGraphemes(s string, maxClusters int) string {
	if maxClusters <= 0 || s == "" {
		return ""
	}
	end := clusterPrefixEnd(s, maxClusters, -1)
	return longestValidPrefix(s[:end])
}

// TruncateGraphemes 行为类似 ClipGraphemes，但当输入确实
// 需要缩短时附加 "…"。当 s 已经放得进 maxClusters 个字簇时
// 原样返回（不添加省略号）。
func TruncateGraphemes(s string, maxClusters int) string {
	clipped := ClipGraphemes(s, maxClusters)
	if clipped == s {
		return s
	}
	return clipped + ellipsis
}

// clipGraphemeBytes 是 ClipGraphemes 的字节预算兄弟函数。它返回
// s 的最长前缀，使其放得进 maxBytes 字节而不切开
// 字簇。Reflect 用它执行字节上限，同时保持
// 组合序列和 emoji 完整。无效的 UTF-8 输入会被修剪到其
// 最长有效前缀。
func clipGraphemeBytes(s string, maxBytes int) string {
	if maxBytes <= 0 || s == "" {
		return ""
	}
	end := clusterPrefixEnd(s, -1, maxBytes)
	return longestValidPrefix(s[:end])
}

// clusterPrefixEnd 从 s 开头遍历字簇，并在满足
// maxClusters 个字簇（>= 0 时）或 maxBytes 字节（>= 0 时）后停止。调用方
// 恰好提供其中一个限制。它返回一个始终是字簇边界的字节偏移
// （因此对有效 UTF-8 输入也是 rune 边界）。
func clusterPrefixEnd(s string, maxClusters, maxBytes int) int {
	end := 0
	count := 0
	for i := 0; i < len(s); {
		next := clusterEnd(s, i)
		if maxBytes >= 0 && next > maxBytes {
			break
		}
		i = next
		end = next
		count++
		if maxClusters >= 0 && count >= maxClusters {
			break
		}
	}
	return end
}

// clusterEnd 返回刚好越过从字节偏移 i 开始的字簇的
// 字节偏移。它至少前进一个 rune，因此即使在畸形 UTF-8 上调用方
// 也总能取得进展（坏字节解码为 RuneError/size 1）。
func clusterEnd(s string, i int) int {
	base, size := utf8.DecodeRuneInString(s[i:])
	j := i + size

	// CRLF 对是一个字簇。
	if base == '\r' && j < len(s) && s[j] == '\n' {
		return j + 1
	}

	// 区域指示符从左到右配对形成旗帜。
	if isRegionalIndicator(base) && j < len(s) {
		if next, sz := utf8.DecodeRuneInString(s[j:]); isRegionalIndicator(next) {
			j += sz
		}
	}

	prev := hangulClass(base)
	for j < len(s) {
		r, sz := utf8.DecodeRuneInString(s[j:])
		switch {
		case r == zwj:
			// ZWJ 在这里把后面的 rune（及其自身的扩展符）粘住。
			j += sz
			if j < len(s) {
				glued, gsz := utf8.DecodeRuneInString(s[j:])
				j += gsz
				prev = hangulClass(glued)
			}
		case isClusterExtend(r):
			j += sz
		case hangulGlues(prev, hangulClass(r)):
			prev = hangulClass(r)
			j += sz
		default:
			return j
		}
	}
	return j
}

// isClusterExtend 报告 r 是否附着到前面的基础 rune：
// 组合标记、变体选择符、连接符和 emoji 修饰符。
func isClusterExtend(r rune) bool {
	if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Mc, r) || unicode.Is(unicode.Me, r) {
		return true
	}
	switch {
	case r >= 0xFE00 && r <= 0xFE0F, // variation selectors
		r == 0x200C,                  // ZWNJ
		r >= 0x1F3FB && r <= 0x1F3FF, // emoji skin-tone modifiers
		r >= 0xE0020 && r <= 0xE007F: // emoji tag characters
		return true
	}
	return false
}

func isRegionalIndicator(r rune) bool { return r >= riStart && r <= riEnd }

// jamoClass 按韩文组合规则（UAX #29 GB6–8）对 rune 分类。
type jamoClass uint8

const (
	jamoNone jamoClass = iota
	jamoL
	jamoV
	jamoT
	jamoLV
	jamoLVT
)

func hangulClass(r rune) jamoClass {
	switch {
	case r >= 0x1100 && r <= 0x115F: // choseong / leading jamo
		return jamoL
	case r >= 0x1160 && r <= 0x11A7: // jungseong / vowel jamo
		return jamoV
	case r >= 0x11A8 && r <= 0x11C2: // jongseong / trailing jamo
		return jamoT
	case r >= 0xAC00 && r <= 0xD7A3: // precomposed syllable
		if (r-0xAC00)%28 == 0 {
			return jamoLV
		}
		return jamoLVT
	}
	return jamoNone
}

// hangulGlues 报告 next 是否可以加入最后一个 jamo 类别为
// prev 的字簇。
func hangulGlues(prev, next jamoClass) bool {
	switch prev {
	case jamoL:
		return next == jamoL || next == jamoV || next == jamoLV || next == jamoLVT
	case jamoLV, jamoV:
		return next == jamoV || next == jamoT
	case jamoLVT, jamoT:
		return next == jamoT
	}
	return false
}

// longestValidPrefix 返回 s 中格式良好的 UTF-8 的最长前缀。
// 这是对传入畸形输入的调用方的防御性保护；对于有效
// 输入，它原样返回 s。
func longestValidPrefix(s string) string {
	for i := 0; i < len(s); {
		r, sz := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && sz == 1 {
			return s[:i]
		}
		i += sz
	}
	return s
}
