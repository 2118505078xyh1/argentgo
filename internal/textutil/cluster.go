// Package textutil 保存工具输出裁剪与 shell 输出治理共享的
// 文本处理原语。它位于两个包之下，使字素簇对齐算法恰好只有一个
// 定义：过去它被复制两份（tools、shell）且逐字节相同，任何一处
// 修改都必须人工同步另一处，shell 副本甚至没有测试守卫。
//
// 本包的裁剪边界规则参考 UAX #29 的一个务实子集，语义与
// internal/memory 的簇裁剪同类，但只使用标准库，不引入任何
// internal 依赖。
//
// 设计约束：
//   - ClusterBoundaryBefore 只回退，ClusterBoundaryAfter 只前进；
//     因此 clipTo 保留的字节只减不增，字节/rune 预算断言始终成立。
//   - 非法 UTF-8 字节（RuneError/size==1）不参与任何粘合，按原样
//     保留，绝不替换或丢弃（与 shell_sanitize.go 的约定一致）。
//   - 有疑问时倾向于把字节粘在一起，因此 exotic 文字可能少数字簇，
//     但绝不会输出悬空的组合标记、孤立 ZWJ 或半个 emoji/旗帜。
package textutil

import (
	"unicode"
	"unicode/utf8"
)

const (
	ClusterZWJ    = 0x200D  // 零宽连接符：粘合前后 rune
	ClusterZWNJ   = 0x200C  // 零宽不连接符：附着到前一个基础 rune
	RIStart       = 0x1F1E6 // 区域指示符 "A"
	RIEnd         = 0x1F1FF // 区域指示符 "Z"
	InvalidRuneID = -1      // 非法字节的哨兵，永不与任何 rune 粘合
)

// ClusterRune 把解码结果归一成粘合规则使用的 rune ID。
// 非法字节（RuneError 且尺寸为 1）映射为 InvalidRuneID，
// 合法的 U+FFFD（RuneError 且尺寸为 3）则按普通 rune 处理。
func ClusterRune(r rune, size int) rune {
	if r == utf8.RuneError && size == 1 {
		return InvalidRuneID
	}
	return r
}

// DecodeRuneAt 返回 s[at:] 起始处的 rune 及其字节数；越界返回 size==0。
func DecodeRuneAt(s string, at int) (rune, int) {
	if at < 0 || at >= len(s) {
		return 0, 0
	}
	return utf8.DecodeRuneInString(s[at:])
}

// DecodeLastRuneBefore 返回 s[:at] 末尾处的 rune 及其字节数；
// 越界返回 size==0。使用标准库可正确处理非法字节（每个坏字节
// 解码为 RuneError/size 1），避免把连续坏字节误当成一个 rune。
func DecodeLastRuneBefore(s string, at int) (rune, int) {
	if at <= 0 || at > len(s) {
		return 0, 0
	}
	return utf8.DecodeLastRuneInString(s[:at])
}

// IsClusterExtend 报告 r 是否附着到前面的基础 rune：组合标记
// （Mn/Mc/Me）、变体选择符、ZWNJ、emoji 肤色修饰符与 emoji 标签字符。
func IsClusterExtend(r rune) bool {
	if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Mc, r) || unicode.Is(unicode.Me, r) {
		return true
	}
	switch {
	case r >= 0xFE00 && r <= 0xFE0F, // 变体选择符
		r == ClusterZWNJ,             // 零宽不连接符
		r >= 0x1F3FB && r <= 0x1F3FF, // emoji 肤色修饰符
		r >= 0xE0020 && r <= 0xE007F: // emoji 标签字符
		return true
	}
	return false
}

// IsRegionalIndicator 报告 r 是否为区域指示符（旗帜字母）。
func IsRegionalIndicator(r rune) bool { return r >= RIStart && r <= RIEnd }

// JamoClass 按韩文组合规则（UAX #29 GB6–8）对 rune 分类。
type JamoClass uint8

const (
	JamoNone JamoClass = iota
	JamoL
	JamoV
	JamoT
	JamoLV
	JamoLVT
)

// HangulClass 返回 rune 的韩文 jamo 类别。
func HangulClass(r rune) JamoClass {
	switch {
	case r >= 0x1100 && r <= 0x115F: // 初声 jamo
		return JamoL
	case r >= 0x1160 && r <= 0x11A7: // 中声 jamo
		return JamoV
	case r >= 0x11A8 && r <= 0x11C2: // 终声 jamo
		return JamoT
	case r >= 0xAC00 && r <= 0xD7A3: // 预组合音节
		if (r-0xAC00)%28 == 0 {
			return JamoLV
		}
		return JamoLVT
	}
	return JamoNone
}

// HangulGlues 报告 next 是否可以加入最后一个 jamo 类别为 prev 的字簇。
func HangulGlues(prev, next JamoClass) bool {
	switch prev {
	case JamoL:
		return next == JamoL || next == JamoV || next == JamoLV || next == JamoLVT
	case JamoLV, JamoV:
		return next == JamoV || next == JamoT
	case JamoLVT, JamoT:
		return next == JamoT
	}
	return false
}

// Glues 报告两个相邻的 rune ID 是否属于同一字簇（即它们之间不是边界）。
// prev/curr 是 ClusterRune 归一后的 rune ID；InvalidRuneID 永不粘合。
func Glues(prev, curr rune) bool {
	if prev == InvalidRuneID || curr == InvalidRuneID {
		return false
	}
	// CRLF 是一个字簇。
	if prev == '\r' && curr == '\n' {
		return true
	}
	// ZWJ 粘合前后两个 rune（ZWJ 链因此连成一体）。
	if prev == ClusterZWJ || curr == ClusterZWJ {
		return true
	}
	// 当前 rune 是附着符（组合标记、变体符、肤色、标签、ZWNJ）。
	if IsClusterExtend(curr) {
		return true
	}
	// 相邻区域指示符组成旗帜；这里刻意保守地把整段 RI 连在一起。
	if IsRegionalIndicator(prev) && IsRegionalIndicator(curr) {
		return true
	}
	// 韩文 jamo GB6–8。
	return HangulGlues(HangulClass(prev), HangulClass(curr))
}

// GluedAt 报告 at 是否位于某个字簇内部。at 必须是 rune 边界；
// 字符串起点/终点恒为边界。非法字节两侧不粘合。
func GluedAt(s string, at int) bool {
	prevR, prevSize := DecodeLastRuneBefore(s, at)
	currR, currSize := DecodeRuneAt(s, at)
	if prevSize == 0 || currSize == 0 {
		return false
	}
	return Glues(ClusterRune(prevR, prevSize), ClusterRune(currR, currSize))
}

// ClusterBoundaryBefore 返回 <= at 的最近字素簇边界。
// 先把 at 回退到 rune 起点，再逐簇回退，直到 at 自身成为簇边界。
// 它从不前进，因此 head 只会缩短。
func ClusterBoundaryBefore(s string, at int) int {
	if at <= 0 {
		return 0
	}
	if at > len(s) {
		at = len(s)
	}
	// 复用 TrimPartialRune 把字节偏移回退到 rune 起点。
	at = TrimPartialRune(s, at)
	for at > 0 && GluedAt(s, at) {
		_, size := DecodeLastRuneBefore(s, at)
		if size <= 0 {
			break
		}
		at -= size
	}
	return at
}

// ClusterBoundaryAfter 返回 >= at 的最近字素簇边界。
// 先把 at 前进到 rune 起点，再逐簇前进，直到 at 自身成为簇边界。
// 它从不回退，因此 tail 只会缩短。
func ClusterBoundaryAfter(s string, at int) int {
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
	for at > 0 && at < len(s) && GluedAt(s, at) {
		_, size := DecodeRuneAt(s, at)
		if size <= 0 {
			break
		}
		at += size
	}
	return at
}

// TrimPartialRune 把字节偏移从 UTF-8 序列的中间回退到起始边界。
func TrimPartialRune(text string, offset int) int {
	if offset <= 0 {
		return 0
	}
	for offset > 0 && offset < len(text) && !utf8.RuneStart(text[offset]) {
		offset--
	}
	return offset
}
