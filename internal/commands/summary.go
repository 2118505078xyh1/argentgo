package commands

// 用于帮助界面和补全列表的命令富文本摘要。
//
// FormatCommandSummary 是一个纯能力：它格式化 Command 的声明性字段，
// 不读取文件、不启动进程，也从不修改 cmd。

import (
	"strings"
)

const (
	summarySeparator  = "  " // cells between the command name and its description
	summaryHintIndent = "  " // leading cells of the argument-hint (second) line
)

// FormatCommandSummary 将 cmd 渲染为宽度受限、一行或两行的
// 摘要，适合帮助界面或补全下拉框。
//
// Layout:
//   - "head" 是 cmd.Name 后跟 cmd.Description，以两个空格分隔；
//     空字段被跳过（仅有名称或仅有描述的 head 也会出现）；
//     allowed).
//   - 当 head 和 cmd.ArgumentHint 均非空时，摘要有
//     两行：head，再加缩进两个空格的提示。否则
//     只有一行，包含存在的那部分；若所有字段都为空，
//     结果为 ""。
//
// 每行被截断到最多 width 个终端单元格（显示列）。
// 截断按 rune 安全处理：绝不拆分 UTF-8 rune，也绝不将东亚
// 宽/全角 rune 拦腰截断 —— CJK 汉字、假名、韩文、全角
// 形式和 emoji 各占两格，仅当两格都放得下时才包含。
// 组合标记及其他零宽 rune 计为零格。内嵌
// 换行和重复空白折叠为单个空格，因此字段始终
// 保持在一行内。width <= 0 没有空间，结果为 ""。
func FormatCommandSummary(cmd Command, width int) string {
	if width <= 0 {
		return ""
	}
	name := summaryField(cmd.Name)
	description := summaryField(cmd.Description)
	hint := summaryField(cmd.ArgumentHint)

	headParts := make([]string, 0, 2)
	if name != "" {
		headParts = append(headParts, name)
	}
	if description != "" {
		headParts = append(headParts, description)
	}
	head := strings.Join(headParts, summarySeparator)

	if head == "" {
		if hint == "" {
			return ""
		}
		return truncateToWidth(hint, width)
	}
	if hint == "" {
		return truncateToWidth(head, width)
	}
	return truncateToWidth(head, width) + "\n" + truncateToWidth(summaryHintIndent+hint, width)
}

// summaryField 将单个声明性字段规范化为单空格、单行的
// 文本，使摘要不会因输入而多出一行。
func summaryField(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// stringDisplayWidth 返回 s 占用的终端单元格数。
func stringDisplayWidth(s string) int {
	w := 0
	for _, r := range s {
		w += runeDisplayWidth(r)
	}
	return w
}

// truncateToWidth 返回 s 的最长前缀，其显示宽度不超过
// width，且只在 rune 边界处截断。宽 rune 从不被拆分。
func truncateToWidth(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if stringDisplayWidth(s) <= width {
		return s
	}
	var b strings.Builder
	w := 0
	for _, r := range s {
		rw := runeDisplayWidth(r)
		if w+rw > width {
			break
		}
		b.WriteRune(r)
		w += rw
	}
	return b.String()
}

// runeDisplayWidth 是某个 rune 占用的终端单元格数：控制字符、
// 组合符和零宽 rune 为 0，东亚宽/全角
// rune（及 emoji）为 2，其余为 1。这些范围覆盖与
// 中文、日文和韩文命令描述相关的 CJK 区块，无需依赖外部库。
func runeDisplayWidth(r rune) int {
	switch {
	case r == 0 || r < 32 || (r >= 0x7f && r < 0xa0):
		return 0
	case isZeroWidthRune(r):
		return 0
	case isWideRune(r):
		return 2
	default:
		return 1
	}
}

func isWideRune(r rune) bool {
	return inRanges(r, wideRuneRanges)
}

func isZeroWidthRune(r rune) bool {
	return inRanges(r, zeroWidthRuneRanges)
}

func inRanges(r rune, ranges [][2]rune) bool {
	for _, rng := range ranges {
		if r < rng[0] {
			return false
		}
		if r <= rng[1] {
			return true
		}
	}
	return false
}

// wideRuneRanges 是 Unicode 东亚宽/全角区块（以及宽
// emoji）的紧凑子集，按升序排列以便 inRanges 提前退出。
var wideRuneRanges = [][2]rune{
	{0x1100, 0x115F}, // Hangul Jamo initial consonants
	{0x231A, 0x231B}, // watch, hourglass
	{0x2329, 0x232A}, // left/right angle bracket
	{0x2E80, 0x303E}, // CJK radicals, Kangxi, CJK symbols and punctuation (incl. U+3000)
	{0x3041, 0x30FF}, // Hiragana and Katakana
	{0x3100, 0x312F}, // Bopomofo
	{0x3131, 0x318E}, // Hangul compatibility Jamo
	{0x3190, 0x31EF}, // CJK miscellaneous / Bopomofo extended
	{0x31F0, 0x321E}, // Katakana phonetic extensions, enclosed CJK
	{0x3220, 0x3247}, // enclosed ideographs
	{0x3250, 0x4DBF}, // CJK extensions, CJK Ext A
	{0x4E00, 0x9FFF}, // CJK Unified Ideographs
	{0xA000, 0xA48C}, // Yi syllables and radicals
	{0xA960, 0xA97C}, // Hangul Jamo extended A
	{0xAC00, 0xD7A3}, // Hangul syllables
	{0xF900, 0xFAFF}, // CJK compatibility ideographs
	{0xFE10, 0xFE19}, // vertical forms
	{0xFE30, 0xFE6F}, // CJK compatibility forms
	{0xFF01, 0xFF60}, // fullwidth forms (incl. fullwidth ASCII)
	{0xFFE0, 0xFFE6}, // fullwidth signs
	{0x16FE0, 0x16FE4},
	{0x17000, 0x187F7}, // Tangut
	{0x1B000, 0x1B2FB}, // Kana supplement
	{0x1F004, 0x1F004},
	{0x1F0CF, 0x1F0CF},
	{0x1F18E, 0x1F18E},
	{0x1F191, 0x1F19A},
	{0x1F200, 0x1F251},
	{0x1F300, 0x1F5FF}, // miscellaneous symbols and pictographs
	{0x1F600, 0x1F64F}, // emoji
	{0x1F680, 0x1F6FF}, // transport and map
	{0x1F900, 0x1F9FF},
	{0x1FA70, 0x1FAFF},
	{0x20000, 0x3FFFD}, // CJK Ext B and beyond
}

// zeroWidthRuneRanges 覆盖那些自身不推进光标的组合标记和
// 格式字符。
var zeroWidthRuneRanges = [][2]rune{
	{0x0300, 0x036F}, // combining diacritical marks
	{0x0483, 0x0489},
	{0x0591, 0x05BD}, {0x05BF, 0x05BF}, {0x05C1, 0x05C2}, {0x05C4, 0x05C5}, {0x05C7, 0x05C7},
	{0x0610, 0x061A}, {0x064B, 0x065F}, {0x0670, 0x0670}, {0x06D6, 0x06DC}, {0x06DF, 0x06E4},
	{0x06E7, 0x06E8}, {0x06EA, 0x06ED},
	{0x0711, 0x0711}, {0x0730, 0x074A}, {0x07A6, 0x07B0}, {0x07EB, 0x07F3},
	{0x0816, 0x0819}, {0x081B, 0x0823}, {0x0825, 0x0827}, {0x0829, 0x082D},
	{0x0859, 0x085B}, {0x08D3, 0x0902},
	{0x093A, 0x093A}, {0x093C, 0x093C}, {0x0941, 0x0948}, {0x094D, 0x094D},
	{0x0951, 0x0957}, {0x0962, 0x0963},
	{0x1AB0, 0x1AFF},   // combining diacritical marks extended
	{0x1DC0, 0x1DFF},   // combining diacritical marks supplement
	{0x200B, 0x200F},   // zero-width space, ZWNJ, ZWJ, LRM, RLM
	{0x202A, 0x202E},   // bidirectional embedding controls
	{0x2060, 0x206F},   // word joiner / invisible operators
	{0x20D0, 0x20FF},   // combining marks for symbols
	{0xFE00, 0xFE0F},   // variation selectors
	{0xFE20, 0xFE2F},   // combining half marks
	{0xFEFF, 0xFEFF},   // zero-width no-break space
	{0xE0100, 0xE01EF}, // variation selectors supplement
}
