// Package textutil 的高层裁剪原语：在保持“rune 预算”语义的前提下，
// 把切点吸附到字素簇边界。它们只收缩、绝不扩张，因此
// utf8.RuneCountInString(结果) <= 预算 始终成立。
//
// 与底层 ClusterBoundaryBefore/After 的区别：
//   - 底层函数原样保留非法字节（RuneError/size==1 不参与粘合）；
//   - 本文件的原语先做 encoded := string([]rune(s))，与 session 现有的
//     truncateRunes 一致：畸形字节被归一成 U+FFFD，之后所有切点都落在
//     合法 UTF-8 与字素簇边界上。
//
// 之所以坚持 rune 预算而不是改成簇预算，是因为调用方的不变量写的是
// RuneCount <= limit；若改成按簇计数会越过上限（一个簇可能含多个 rune）。
// 正确做法是先按 rune 算出切点，再用簇边界把它向内吸附。
package textutil

import "unicode/utf8"

// byteOffsetAfterRunes 返回从合法 UTF-8 串 encoded 起点跳过 count 个
// rune 之后的字节偏移。encoded 必须来自 []rune 重编码，故不含非法字节；
// count 越界时钳制到 [0, len(encoded)]。
func byteOffsetAfterRunes(encoded string, count int) int {
	if count <= 0 {
		return 0
	}
	off := 0
	for i := 0; i < count && off < len(encoded); i++ {
		_, size := utf8.DecodeRuneInString(encoded[off:])
		if size == 0 {
			break
		}
		off += size
	}
	return off
}

// ClipRunes 把文本裁到至多 maxRunes 个 rune。
//
// 语义与 session.truncateRunes 的 []rune 重编码一致：先把 s 重编码为
// encoded（畸形字节归一 U+FFFD）。若 rune 数不超过预算则原样返回 encoded；
// 否则先算出“保留前 maxRunes 个 rune”的字节偏移 k（即默认切点），
// 再用 ClusterBoundaryBefore 把 k 向前吸附到字素簇边界，返回 encoded[:cut]。
// 因为 cut <= k，结果只会更短：合法 UTF-8、RuneCount <= maxRunes，
// 且不会把组合标记、ZWJ、旗帜或 emoji 从中间劈开。
func ClipRunes(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(s)
	encoded := string(runes)
	if len(runes) <= maxRunes {
		return encoded
	}
	k := byteOffsetAfterRunes(encoded, maxRunes)
	return encoded[:ClusterBoundaryBefore(encoded, k)]
}

// ClipRunesTail 取文本末尾至多 maxRunes 个 rune，起点用
// ClusterBoundaryAfter 向后（向右）吸附到字素簇边界，只收缩。
//
// 先算出“跳过前 len(runes)-maxRunes 个 rune”的起点偏移 bs，再把 bs 吸附
// 到簇边界 start。start >= bs，故保留的尾巴只会更短：结果合法 UTF-8、
// RuneCount <= maxRunes，且不会在开头留下悬空的组合标记或半个 emoji。
func ClipRunesTail(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(s)
	n := len(runes)
	if n <= maxRunes {
		return string(runes)
	}
	encoded := string(runes)
	bs := byteOffsetAfterRunes(encoded, n-maxRunes)
	start := ClusterBoundaryAfter(encoded, bs)
	return encoded[start:]
}

// ClipWindow 供搜索高亮使用：取从 rune 下标 startRune 开始、长度
// runeLen 个 rune 的窗口，起点用 ClusterBoundaryAfter 向后吸附、
// 终点用 ClusterBoundaryBefore 向前吸附，两侧都只收缩，返回字簇完整的
// 子串。越界参数被钳制；窗口落入单个字簇内部（吸附后首尾交叉）时返回空串。
func ClipWindow(s string, startRune, runeLen int) string {
	if runeLen <= 0 {
		return ""
	}
	runes := []rune(s)
	n := len(runes)
	if startRune < 0 {
		startRune = 0
	}
	if startRune >= n {
		return ""
	}
	// endRune = min(startRune+runeLen, n)，同时防 int 溢出。
	endRune := startRune + runeLen
	if endRune < startRune || endRune > n {
		endRune = n
	}
	encoded := string(runes)
	bs := byteOffsetAfterRunes(encoded, startRune)
	be := byteOffsetAfterRunes(encoded, endRune)
	start := ClusterBoundaryAfter(encoded, bs)
	end := ClusterBoundaryBefore(encoded, be)
	if start >= end {
		return ""
	}
	return encoded[start:end]
}
