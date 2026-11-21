package tools

// 本文件为 clipTo 提供按"字素簇"(grapheme cluster) 对齐的裁剪边界，
// 仅使用标准库实现。规则参考 UAX #29 的一个务实子集，语义与
// internal/memory 的簇裁剪同类，但这里是 tools 包内的独立实现，
// 不引入跨包依赖。
//
// 设计约束：
//   - clusterBoundaryBefore 只回退，clusterBoundaryAfter 只前进；
//     因此 clipTo 保留的字节只减不增，字节/rune 预算断言始终成立。
//   - 非法 UTF-8 字节（RuneError/size==1）不参与任何粘合，按原样
//     保留，绝不替换或丢弃（与 shell_sanitize.go 的约定一致）。
//   - 有疑问时倾向于把字节粘在一起，因此 exotic 文字可能少数字簇，
//     但绝不会输出悬空的组合标记、孤立 ZWJ 或半个 emoji/旗帜。

import (
	"unicode"
	"unicode/utf8"
)

const (
	clusterZWJ    = 0x200D  // 零宽连接符：粘合前后 rune
	clusterZWNJ   = 0x200C  // 零宽不连接符：附着到前一个基础 rune
	riStart       = 0x1F1E6 // 区域指示符 "A"
	riEnd         = 0x1F1FF // 区域指示符 "Z"
	invalidRuneID = -1      // 非法字节的哨兵，永不与任何 rune 粘合
)

// clusterRune 把解码结果归一成粘合规则使用的 rune ID。
// 非法字节（RuneError 且尺寸为 1）映射为 invalidRuneID，
// 合法的 U+FFFD（RuneError 且尺寸为 3）则按普通 rune 处理。
func clusterRune(r rune, size int) rune {
	if r == utf8.RuneError && size == 1 {
		return invalidRuneID
	}
	return r
}

// decodeRuneAt 返回 s[at:] 起始处的 rune 及其字节数；越界返回 size==0。
func decodeRuneAt(s string, at int) (rune, int) {
	if at < 0 || at >= len(s) {
		return 0, 0
	}
	return utf8.DecodeRuneInString(s[at:])
}

// decodeLastRuneBefore 返回 s[:at] 末尾处的 rune 及其字节数；
// 越界返回 size==0。使用标准库可正确处理非法字节（每个坏字节
// 解码为 RuneError/size 1），避免把连续坏字节误当成一个 rune。
func decodeLastRuneBefore(s string, at int) (rune, int) {
	if at <= 0 || at > len(s) {
		return 0, 0
	}
	return utf8.DecodeLastRuneInString(s[:at])
}

// isClusterExtend 报告 r 是否附着到前面的基础 rune：组合标记
// （Mn/Mc/Me）、变体选择符、ZWNJ、emoji 肤色修饰符与 emoji 标签字符。
func isClusterExtend(r rune) bool {
	if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Mc, r) || unicode.Is(unicode.Me, r) {
		return true
	}
	switch {
	case r >= 0xFE00 && r <= 0xFE0F, // 变体选择符
		r == clusterZWNJ,             // 零宽不连接符
		r >= 0x1F3FB && r <= 0x1F3FF, // emoji 肤色修饰符
		r >= 0xE0020 && r <= 0xE007F: // emoji 标签字符
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
	case r >= 0x1100 && r <= 0x115F: // 初声 jamo
		return jamoL
	case r >= 0x1160 && r <= 0x11A7: // 中声 jamo
		return jamoV
	case r >= 0x11A8 && r <= 0x11C2: // 终声 jamo
		return jamoT
	case r >= 0xAC00 && r <= 0xD7A3: // 预组合音节
		if (r-0xAC00)%28 == 0 {
			return jamoLV
		}
		return jamoLVT
	}
	return jamoNone
}

// hangulGlues 报告 next 是否可以加入最后一个 jamo 类别为 prev 的字簇。
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

// glues 报告两个相邻的 rune ID 是否属于同一字簇（即它们之间不是边界）。
// prev/curr 是 clusterRune 归一后的 rune ID；invalidRuneID 永不粘合。
func glues(prev, curr rune) bool {
	if prev == invalidRuneID || curr == invalidRuneID {
		return false
	}
	// CRLF 是一个字簇。
	if prev == '\r' && curr == '\n' {
		return true
	}
	// ZWJ 粘合前后两个 rune（ZWJ 链因此连成一体）。
	if prev == clusterZWJ || curr == clusterZWJ {
		return true
	}
	// 当前 rune 是附着符（组合标记、变体符、肤色、标签、ZWNJ）。
	if isClusterExtend(curr) {
		return true
	}
	// 相邻区域指示符组成旗帜；这里刻意保守地把整段 RI 连在一起。
	if isRegionalIndicator(prev) && isRegionalIndicator(curr) {
		return true
	}
	// 韩文 jamo GB6–8。
	return hangulGlues(hangulClass(prev), hangulClass(curr))
}

// gluedAt 报告 at 是否位于某个字簇内部。at 必须是 rune 边界；
// 字符串起点/终点恒为边界。非法字节两侧不粘合。
func gluedAt(s string, at int) bool {
	prevR, prevSize := decodeLastRuneBefore(s, at)
	currR, currSize := decodeRuneAt(s, at)
	if prevSize == 0 || currSize == 0 {
		return false
	}
	return glues(clusterRune(prevR, prevSize), clusterRune(currR, currSize))
}

// clusterBoundaryBefore 返回 <= at 的最近字素簇边界。
// 先把 at 回退到 rune 起点，再逐簇回退，直到 at 自身成为簇边界。
// 它从不前进，因此 head 只会缩短。
func clusterBoundaryBefore(s string, at int) int {
	if at <= 0 {
		return 0
	}
	if at > len(s) {
		at = len(s)
	}
	// 复用既有的 trimPartialRune 把字节偏移回退到 rune 起点。
	at = trimPartialRune(s, at)
	for at > 0 && gluedAt(s, at) {
		_, size := decodeLastRuneBefore(s, at)
		if size <= 0 {
			break
		}
		at -= size
	}
	return at
}

// clusterBoundaryAfter 返回 >= at 的最近字素簇边界。
// 先把 at 前进到 rune 起点，再逐簇前进，直到 at 自身成为簇边界。
// 它从不回退，因此 tail 只会缩短。
func clusterBoundaryAfter(s string, at int) int {
	if at <= 0 {
		return 0
	}
	if at >= len(s) {
		return len(s)
	}
	// 先前进到 rune 起点（与 clipTo 中的 RuneStart 循环等价，
	// 这里再执行一次使本函数对任意输入都自洽）。
	for at < len(s) && !utf8.RuneStart(s[at]) {
		at++
	}
	if at >= len(s) {
		return len(s)
	}
	for at > 0 && at < len(s) && gluedAt(s, at) {
		_, size := decodeRuneAt(s, at)
		if size <= 0 {
			break
		}
		at += size
	}
	return at
}
