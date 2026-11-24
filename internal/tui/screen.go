package tui

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// Color 是前景/背景 SGR 对。零值是终端
// 默认颜色，这也是大多数文本使用的颜色。
type Color struct {
	fg string
	bg string
}

// 八种基础颜色加上亮灰/白对，仿照 Rust
// 的屏幕模型（`crates/core/src/screen.rs`）。
var (
	ColorDefault = Color{}
	ColorRed     = Color{"31", "41"}
	ColorGreen   = Color{"32", "42"}
	ColorYellow  = Color{"33", "43"}
	ColorBlue    = Color{"34", "44"}
	ColorMagenta = Color{"35", "45"}
	ColorCyan    = Color{"36", "46"}
	ColorGray    = Color{"90", "100"}
	ColorWhite   = Color{"97", "107"}
	// ColorSilver 是基础 SGR 白色（37），在深色背景上作为 muted 正文
	// 比 SGR 90 的深灰清晰得多，且仍属 16 色基础调色板（非 256/RGB），
	// 满足主题门禁。用于默认深色主题的次要文本。
	ColorSilver = Color{"37", "47"}
)

// Fixed 从 256 色调色板返回一个颜色。
func Fixed(n uint8) Color {
	index := strconv.Itoa(int(n))
	return Color{"38;5;" + index, "48;5;" + index}
}

// RGB 返回一个 24 位颜色。
func RGB(r, g, b uint8) Color {
	body := strconv.Itoa(int(r)) + ";" + strconv.Itoa(int(g)) + ";" + strconv.Itoa(int(b))
	return Color{"38;2;" + body, "48;2;" + body}
}

// Style 是某个 span 的绘制方式。
type Style struct {
	Fg      Color
	Bg      Color
	Bold    bool
	Dim     bool
	Reverse bool
}

// SGR 是开启该样式的转义序列，无样式时为 ""。
func (s Style) SGR() string {
	var out strings.Builder
	sep := false
	add := func(code string) {
		if sep {
			out.WriteByte(';')
		}
		out.WriteString(code)
		sep = true
	}
	if s.Fg.fg != "" {
		add(s.Fg.fg)
	}
	if s.Bold {
		add("1")
	}
	if s.Dim {
		add("2")
	}
	if s.Reverse {
		add("7")
	}
	if s.Bg.bg != "" {
		add(s.Bg.bg)
	}
	if !sep {
		return ""
	}
	return "\x1b[" + out.String() + "m"
}

// BoldStyle 返回给定颜色的粗体样式。
func BoldStyle(c Color) Style { return Style{Fg: c, Bold: true} }

// Span 是带有一种样式的连续文本。
type Span struct {
	Text  string
	Style Style
}

// Width 是该 span 覆盖的单元格数。
func (s Span) Width() int { return TextWidth(s.Text) }

// Line 是一个已渲染的行。
type Line struct {
	Spans []Span
}

// NewLine 创建一个空行。
func NewLine() Line { return Line{} }

// PlainLine 是无样式文本的一行。
func PlainLine(text string) Line { return Line{Spans: []Span{{Text: text}}} }

// Push 追加带样式的文本。
func (l *Line) Push(text string, style Style) {
	if text == "" {
		return
	}
	l.Spans = append(l.Spans, Span{Text: text, Style: style})
}

// PushSpan 追加一个 span。
func (l *Line) PushSpan(s Span) {
	if s.Text == "" {
		return
	}
	l.Spans = append(l.Spans, s)
}

// Width 是该行覆盖的单元格数。
func (l Line) Width() int {
	total := 0
	for _, span := range l.Spans {
		total += span.Width()
	}
	return total
}

// Text 是该行的无样式内容。
func (l Line) Text() string {
	var out strings.Builder
	for _, span := range l.Spans {
		out.WriteString(span.Text)
	}
	return out.String()
}

// Fill 用带样式的空格填充该行，直到覆盖 width 个单元格。
func (l *Line) Fill(width int, style Style) {
	if used := l.Width(); used < width {
		l.Spans = append(l.Spans, Span{Text: strings.Repeat(" ", width-used), Style: style})
	}
}

// RuneWidth 是一个 rune 占用的终端单元格数。
//
// 零宽：组合标记和变体选择符。两单元格：东亚
// 范围以及实际出现在正文和路径中的 emoji。其他
// 可打印字符都是一单元格。这与 Rust 参考表采用相同的近似，
// 有意保持精简。
func RuneWidth(r rune) int {
	code := int32(r)
	switch {
	case code >= 0x0300 && code <= 0x036F,
		code >= 0x0483 && code <= 0x0489,
		code >= 0x200B && code <= 0x200F,
		code >= 0x20D0 && code <= 0x20FF,
		code >= 0xFE00 && code <= 0xFE0F,
		code >= 0xFE20 && code <= 0xFE2F,
		code >= 0xE0100 && code <= 0xE01EF:
		return 0
	}
	if code < 0x20 || code == 0x7F {
		return 0
	}
	switch {
	case code >= 0x1100 && code <= 0x115F,
		code >= 0x2E80 && code <= 0x303E,
		code >= 0x3041 && code <= 0x33FF,
		code >= 0x3400 && code <= 0x4DBF,
		code >= 0x4E00 && code <= 0x9FFF,
		code >= 0xA000 && code <= 0xA4CF,
		code >= 0xA960 && code <= 0xA97F,
		code >= 0xAC00 && code <= 0xD7A3,
		code >= 0xF900 && code <= 0xFAFF,
		code >= 0xFE10 && code <= 0xFE19,
		code >= 0xFE30 && code <= 0xFE6F,
		code >= 0xFF00 && code <= 0xFF60,
		code >= 0xFFE0 && code <= 0xFFE6,
		code >= 0x1F300 && code <= 0x1F64F,
		code >= 0x1F680 && code <= 0x1F6FF,
		code >= 0x1F900 && code <= 0x1F9FF,
		code >= 0x1FA70 && code <= 0x1FAFF,
		code >= 0x20000 && code <= 0x3FFFD:
		return 2
	}
	return 1
}

const zwj = 0x200D

// isRegionalIndicator 报告 r 是否为成对的旗帜字母之一。
func isRegionalIndicator(r rune) bool {
	return r >= 0x1F1E6 && r <= 0x1F1FF
}

// isExtend 报告 r 是否在同一字素簇中附着于其前的
// rune（组合标记、变体选择符、ZWJ、emoji
// 肤色修饰符等）。C0 控制字符和 DEL 被有意排除：
// 它们在 RuneWidth 中为零宽，但不得把两个簇粘在一起。
func isExtend(r rune) bool {
	if r >= 0x1F3FB && r <= 0x1F3FF { // emoji skin-tone modifiers
		return true
	}
	return r >= 0x20 && r != 0x7F && RuneWidth(r) == 0
}

// nextGrapheme 返回 s 中从 start 开始的字素簇的字节范围
// [start, end) 及其显示宽度。这是有意简化的
// UAX #29 扩展字素簇近似，只覆盖
// 对终端单元格重要的情况：
//
//   - Base + (Extend | ZWJ Base)* 是一个簇，其宽度取其中
//     最宽的 rune。ZWJ 把后面的图形拉进同一簇，
//     因此 👨‍👩‍👧 测得两个单元格而非六个。
//   - 两个相邻的区域指示符组成一面旗帜，始终两个单元格。
//   - ASCII 是单字节、单单元格的簇（快速路径）。
//
// RuneWidth 仍是单个 rune 宽度的权威。
func nextGrapheme(s string, start int) (end, width int) {
	if b := s[start]; b < utf8.RuneSelf {
		if b < 0x20 || b == 0x7F {
			// 控制字符为零宽，且不得把簇粘在一起。
			return start + 1, 0
		}
		end, width = start+1, 1
		// 快速路径：后面只有 ASCII 的 ASCII 基础字符不可能吸收
		// Extend rune（每个 Extend rune 都是非 ASCII）。非 ASCII 的后继
		// 会落到下面，使组合标记、VS 或 ZWJ 序列并入基础字符。
		if end == len(s) || s[end] < utf8.RuneSelf {
			return end, width
		}
	} else {
		r, size := utf8.DecodeRuneInString(s[start:])
		end, width = start+size, RuneWidth(r)

		// 一对相邻的区域指示符是一面旗帜：两个单元格，即使
		// 每个指示符单独只有一个单元格宽。
		if isRegionalIndicator(r) && end < len(s) {
			if r2, size2 := utf8.DecodeRuneInString(s[end:]); isRegionalIndicator(r2) {
				end += size2
				width = 2
				for end < len(s) {
					r3, size3 := utf8.DecodeRuneInString(s[end:])
					if !isExtend(r3) {
						break
					}
					end += size3
				}
				return end, width
			}
		}
	}

	afterZWJ := false
	for end < len(s) {
		r2, size2 := utf8.DecodeRuneInString(s[end:])
		switch {
		case r2 == zwj:
			afterZWJ = true
		case isExtend(r2):
			// 扩展符留在当前簇中。
		case afterZWJ && r2 >= 0x20 && r2 != 0x7F:
			// 由 preceding ZWJ 连接到簇的基础字符。
			afterZWJ = false
		default:
			return end, width
		}
		if w := RuneWidth(r2); w > width {
			width = w
		}
		end += size2
	}
	return end, width
}

// TextWidth 是字符串占用的单元格数，按字素簇计数，
// 使 ZWJ emoji 或旗帜不会被重复计算。
func TextWidth(text string) int {
	total := 0
	for i := 0; i < len(text); {
		if text[i] < utf8.RuneSelf {
			// 批量 ASCII 路径：可打印字节为一个单元格，控制字符为零。
			// 在后继字节非 ASCII 的字节之前停止：该字节可能
			// 开启一个跨边界延续的簇（组合
			// 标记、变体选择符或 ZWJ 连接的 emoji），只有
			// nextGrapheme 能正确测量它。
			start := i
			for i < len(text) && text[i] < utf8.RuneSelf &&
				(i+1 == len(text) || text[i+1] < utf8.RuneSelf) {
				if text[i] >= 0x20 && text[i] != 0x7F {
					total++
				}
				i++
			}
			if i > start {
				continue
			}
		}
		next, width := nextGrapheme(text, i)
		total += width
		i = next
	}
	return total
}

// Clip 切分 text，使其最多占 max 个单元格。跨越
// 边界的簇被整体丢弃，使 ZWJ emoji、旗帜和组合
// 序列绝不会被从中间切开。
func Clip(text string, max int) string {
	if max <= 0 {
		return ""
	}
	end, used := 0, 0
	for pos := 0; pos < len(text); {
		next, width := nextGrapheme(text, pos)
		if used+width > max {
			break
		}
		used += width
		end = next
		pos = next
	}
	return text[:end]
}

// Truncate 裁剪文本并用省略号标记切口。
func Truncate(text string, max int) string {
	if TextWidth(text) <= max {
		return text
	}
	if max <= 0 {
		return ""
	}
	return Clip(text, max-1) + "…"
}

// Pad 裁剪或填充文本，使其恰好覆盖 width 个单元格。
func Pad(text string, width int) string {
	clipped := Clip(text, width)
	if used := TextWidth(clipped); used < width {
		return clipped + strings.Repeat(" ", width-used)
	}
	return clipped
}

// Wrap 把文本断为每行最多 width 个单元格，优先在行尾
// 的空格处断开。CJK 没有空格，因此硬断是中文的
// 正常路径，而非边缘情况。换行发生在字素
// 边界：ZWJ emoji、旗帜或组合序列绝不会被折在
// 中间。
func Wrap(text string, width int) []string {
	if width < 1 {
		width = 1
	}
	var out []string
	curStart, curEnd, curWidth := 0, 0, 0
	lastSpace := -1
	for pos := 0; pos < len(text); {
		next, cell := nextGrapheme(text, pos)
		r, _ := utf8.DecodeRuneInString(text[pos:next])
		if curWidth+cell > width && curEnd > curStart {
			if lastSpace > curStart {
				head := strings.TrimRight(text[curStart:lastSpace], " ")
				// 第一个词之前的前导空白修剪为空。
				// 输出它会在缩进行上方画出多余的空行。
				if head != "" {
					out = append(out, head)
				}
				tail := lastSpace
				for tail < curEnd && text[tail] == ' ' {
					tail++
				}
				curStart = tail
				curWidth = TextWidth(text[curStart:curEnd])
			} else {
				out = append(out, text[curStart:curEnd])
				curStart = pos
				curWidth = 0
			}
			lastSpace = -1
		}
		curEnd = next
		curWidth += cell
		if r == ' ' {
			lastSpace = pos
		}
		pos = next
	}
	if curEnd > curStart {
		out = append(out, text[curStart:curEnd])
	}
	if len(out) == 0 {
		out = []string{""}
	}
	return out
}

// compactCount 把 token 计数渲染为 942 / 8.2k / 1.6M。
func compactCount(n int) string {
	if n < 0 {
		n = 0
	}
	switch {
	case n < 1000:
		return strconv.Itoa(n)
	case n < 1_000_000:
		k := float64(n) / 1000
		if k >= 10 {
			return strconv.Itoa(int(k)) + "k"
		}
		return strconv.FormatFloat(k, 'f', 1, 64) + "k"
	}
	m := float64(n) / 1_000_000
	if m >= 10 {
		return strconv.Itoa(int(m)) + "M"
	}
	return strconv.FormatFloat(m, 'f', 1, 64) + "M"
}

// clock 把经过时间渲染为 0:07 / 12:04 / 1:02:03。
func clock(seconds int) string {
	if seconds < 0 {
		seconds = 0
	}
	if seconds < 3600 {
		return strconv.Itoa(seconds/60) + ":" + twoDigits(seconds%60)
	}
	return strconv.Itoa(seconds/3600) + ":" + twoDigits((seconds%3600)/60) + ":" + twoDigits(seconds%60)
}

func twoDigits(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}
