package tui

import "math"

// minInkContrast 是 WCAG AA 对大号/粗体文字的下限（3:1）。徽章标签
// 短且加粗，因此这是墨色在我们可以对其置之不理之前
// 必须达到的比率。
const minInkContrast = 3.0

// parseHexColor 把 #rgb、#rgba、#rrggbb 和 #rrggbbaa 解码为 8 位
// 通道。Alpha 若存在仍必须格式良好但会被忽略：
// 亮度定义在不透明颜色上，而渲染器把徽章画在
// 不透明的终端行上。布尔值报告输入是否属于
// 受支持的、格式良好的形式。
func parseHexColor(s string) (r, g, b uint8, ok bool) {
	if len(s) == 0 || s[0] != '#' {
		return 0, 0, 0, false
	}
	digits := s[1:]
	switch len(digits) {
	case 3:
		// 把 #abc 展开为 #aabbcc，使下面每种形式都至少六位宽。
		digits = string([]byte{digits[0], digits[0], digits[1], digits[1], digits[2], digits[2]})
	case 4:
		// 把 #abcd 展开为 #aabbccdd；最后一对会被校验但不使用。
		digits = string([]byte{digits[0], digits[0], digits[1], digits[1], digits[2], digits[2], digits[3], digits[3]})
	case 6, 8: // 8 is rrggbbaa; the trailing pair is validated but unused.
	default:
		return 0, 0, 0, false
	}

	nibble := func(c byte) (byte, bool) {
		switch {
		case c >= '0' && c <= '9':
			return c - '0', true
		case c >= 'a' && c <= 'f':
			return c - 'a' + 10, true
		case c >= 'A' && c <= 'F':
			return c - 'A' + 10, true
		default:
			return 0, false
		}
	}
	channel := func(hi, lo byte) (uint8, bool) {
		h, okH := nibble(hi)
		l, okL := nibble(lo)
		if !okH || !okL {
			return 0, false
		}
		return h<<4 | l, true
	}
	if r, ok = channel(digits[0], digits[1]); !ok {
		return 0, 0, 0, false
	}
	if g, ok = channel(digits[2], digits[3]); !ok {
		return 0, 0, 0, false
	}
	if b, ok = channel(digits[4], digits[5]); !ok {
		return 0, 0, 0, false
	}
	if len(digits) == 8 {
		if _, ok = nibble(digits[6]); !ok {
			return 0, 0, 0, false
		}
		if _, ok = nibble(digits[7]); !ok {
			return 0, 0, 0, false
		}
	}
	return r, g, b, true
}

// relativeLuminance 是不透明 sRGB 颜色的 WCAG 2.0 相对
// 亮度：先对每个通道线性化，再应用 sRGB 亮度权重。
func relativeLuminance(r, g, b uint8) float64 {
	linear := func(v uint8) float64 {
		c := float64(v) / 255
		if c <= 0.04045 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*linear(r) + 0.7152*linear(g) + 0.0722*linear(b)
}

// contrast 计算 WCAG 比率而不暴露成功标志。布尔值
// 在任一颜色不是受支持的十六进制形式时为 false。
func contrast(hexA, hexB string) (float64, bool) {
	ar, ag, ab, aok := parseHexColor(hexA)
	br, bgG, bb, bok := parseHexColor(hexB)
	if !aok || !bok {
		return 0, false
	}
	la, lb := relativeLuminance(ar, ag, ab), relativeLuminance(br, bgG, bb)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05), true
}

// ContrastRatio 返回两个十六进制颜色之间的 WCAG 对比度，范围
// [0, 21]。黑底白字为 21，颜色与自身为 1。无效
// 输入产生 0 这个保守值：用它做阈值的调用方会把该
// 颜色对视为不可读，而不是猜测。
func ContrastRatio(hexA, hexB string) float64 {
	ratio, ok := contrast(hexA, hexB)
	if !ok {
		return 0
	}
	return ratio
}

// LegibleInk 选择画在 bg 上的徽章墨色。当原墨色
// 已达到 minInkContrast、当 alt 并非严格更优，或
// 当任一颜色无法解析时，保留原墨色 —— 因此不能信任输入的调用方
// 永远不会改变它们原本要绘制的内容。
//
// 内置调色板存储的是 SGR 码而非十六进制，因此该辅助函数
// 尚未接入徽章渲染器；见文件末尾的接线说明。
func LegibleInk(bg, ink, alt string) string {
	inkRatio, ok := contrast(bg, ink)
	if !ok || inkRatio >= minInkContrast {
		return ink
	}
	altRatio, ok := contrast(bg, alt)
	if !ok || altRatio <= inkRatio {
		return ink
	}
	return alt
}

// 接线说明（有意不在此应用）：theme.go 中的 Palette 角色是
// "33"/"43" 这样的 SGR 字符串，而非十六进制，而且没有按主题的徽章
// 背景角色可供比较。在渲染时应用 LegibleInk，首先需要
// 在 Palette 上仅对非内置主题填充十六进制的墨色/背景对；
// 然后调用点将是 Model.attemptLinesLocked（render.go 中的 "⟳"
// 尝试徽章）和 renderEntryLocked 中的通知/横幅墨色选择。
// 内置调色板保持其 SGR 颜色，因此渲染器不变，
// 每个随附主题的输出保持完全一致。
