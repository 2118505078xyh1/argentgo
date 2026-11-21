package tui

import (
	"unicode"
	"unicode/utf8"
)

// VimMotion 提供一组编辑器级 vim motion 原语。它是纯函数：默认不接线到
// 输入循环，调用方决定何时启用（opt-in）。函数不修改任何入参，对相同输入
// 总是返回相同结果。
//
// 参数与返回值：
//   - text：可能被编辑的文本，可包含换行；motion 本身不改变文本，因此
//     newText 总是等于 text。
//   - cursor：以字节为单位的索引。它总是被当作"rune 感知"的位置使用——
//     若传入的值落在某个 rune 的中间，会向下吸附到该 rune 的起始边界。
//   - key：单个 motion 键，例如 "h"、"l"、"0"、"$"、"w"、"b"、"i"、"a"。
//   - newCursor：变换后的字节索引，始终被钳制在 [0, len(newText)] 内且
//     落在合法的 rune 边界上。
//   - handled：key 是否为已实现的 motion；未识别的键返回 false。
//
// 实现的 motion：
//   - h / l：左 / 右移动一个 rune（rune 边界安全）。
//   - 0 / ^：当前行首 / 当前行首个非空白字符。
//   - $ / A：当前行尾 / 追加到行尾。
//   - w / b：下一个词首 / 上一个词首（词按 vim 的关键字与标点两类划分，
//     CJK 字符按 unicode.IsLetter 视为关键字）。
//   - i / a：在光标处插入 / 在光标后追加（仅定位，文本不变）。
//   - I：在当前行首个非空白字符处插入。
//
// j / k 属于跨行历史导航，而编辑器输入框是单行输入，因此它们返回
// handled=false，由调用方决定如何回退（如切换到历史浏览）。
func VimMotion(text string, cursor int, key string) (newText string, newCursor int, handled bool) {
	cur := clampRuneBoundary(text, cursor)
	switch key {
	case "h":
		return text, prevRuneStart(text, cur), true
	case "l":
		return text, nextRuneStart(text, cur), true
	case "0":
		return text, vimLineStart(text, cur), true
	case "^":
		return text, firstNonBlank(text, cur), true
	case "$":
		return text, vimLineEnd(text, cur), true
	case "w":
		return text, nextWordStart(text, cur), true
	case "b":
		return text, prevWordStart(text, cur), true
	case "i":
		return text, cur, true
	case "a":
		return text, nextRuneStart(text, cur), true
	case "I":
		return text, firstNonBlank(text, cur), true
	case "A":
		return text, vimLineEnd(text, cur), true
	}
	return text, cur, false
}

// clampRuneBoundary 把字节索引钳制到 [0, len(text)]，并向下吸附到最近的
// rune 起始位置，保证结果永远落在合法 rune 边界上。
func clampRuneBoundary(text string, cursor int) int {
	if cursor < 0 {
		return 0
	}
	if cursor >= len(text) {
		return len(text)
	}
	for cursor > 0 && !utf8.RuneStart(text[cursor]) {
		cursor--
	}
	return cursor
}

// prevRuneStart 返回光标前一个 rune 的起始字节索引。
func prevRuneStart(text string, cursor int) int {
	if cursor <= 0 {
		return 0
	}
	_, size := utf8.DecodeLastRuneInString(text[:cursor])
	return cursor - size
}

// nextRuneStart 返回光标后一个 rune 的起始字节索引。
func nextRuneStart(text string, cursor int) int {
	if cursor >= len(text) {
		return len(text)
	}
	_, size := utf8.DecodeRuneInString(text[cursor:])
	return cursor + size
}

// vimLineStart 返回光标所在行的行首字节索引（换行符之后的位置）。
func vimLineStart(text string, cursor int) int {
	for i := cursor - 1; i >= 0; i-- {
		if text[i] == '\n' {
			return i + 1
		}
	}
	return 0
}

// vimLineEnd 返回光标所在行的行尾字节索引（换行符之前，或文本末尾）。
func vimLineEnd(text string, cursor int) int {
	for i := cursor; i < len(text); i++ {
		if text[i] == '\n' {
			return i
		}
	}
	return len(text)
}

// firstNonBlank 返回当前行的第一个非空白（空格或制表符）字符的字节索引；
// 空行或整行空白时返回行首。
func firstNonBlank(text string, cursor int) int {
	i := vimLineStart(text, cursor)
	for i < len(text) {
		r, size := utf8.DecodeRuneInString(text[i:])
		if r == '\n' || r == '\r' || (r != ' ' && r != '\t') {
			break
		}
		i += size
	}
	return i
}

// isVimWordBlank 报告 rune 是否作为词间空白：空白或行结束符。
func isVimWordBlank(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\r', '\v', '\f':
		return true
	}
	return false
}

// vimWordClass 把非空白 rune 分成两类：关键字（字母、数字、下划线）与
// 其他标点。两类之间的边界也是词边界，这与 vim 对 word 的定义一致。
func vimWordClass(r rune) int {
	if r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) {
		return 1
	}
	return 2
}

// nextWordStart 返回光标之后下一个词首的字节索引。若光标位于空白中，
// 则跳过空白落在下一个词首；若位于词内，则先跨过当前词（含跨类），
// 再跳过空白落在下一个词首。到达文本末尾时返回 len(text)。
func nextWordStart(text string, cursor int) int {
	n := len(text)
	i := cursor
	if i >= n {
		return n
	}
	r, size := utf8.DecodeRuneInString(text[i:])
	if isVimWordBlank(r) {
		for i < n {
			r, size = utf8.DecodeRuneInString(text[i:])
			if !isVimWordBlank(r) {
				break
			}
			i += size
		}
		return i
	}
	cls := vimWordClass(r)
	for i < n {
		r, size = utf8.DecodeRuneInString(text[i:])
		if isVimWordBlank(r) || vimWordClass(r) != cls {
			break
		}
		i += size
	}
	for i < n {
		r, size = utf8.DecodeRuneInString(text[i:])
		if !isVimWordBlank(r) {
			break
		}
		i += size
	}
	return i
}

// prevWordStart 返回光标之前上一个词首的字节索引。先向前跳过空白，
// 再回退到所在（或紧邻的前一个）词的词首。到达文本开头时返回 0。
func prevWordStart(text string, cursor int) int {
	if cursor <= 0 {
		return 0
	}
	if cursor > len(text) {
		cursor = len(text)
	}
	i := cursor
	for i > 0 {
		r, size := utf8.DecodeLastRuneInString(text[:i])
		if !isVimWordBlank(r) {
			break
		}
		i -= size
	}
	if i <= 0 {
		return 0
	}
	r, _ := utf8.DecodeLastRuneInString(text[:i])
	cls := vimWordClass(r)
	for i > 0 {
		r2, size2 := utf8.DecodeLastRuneInString(text[:i])
		if isVimWordBlank(r2) || vimWordClass(r2) != cls {
			break
		}
		i -= size2
	}
	return i
}
