package agent

import (
	"strings"
	"unicode"
)

// MagicKeyword 描述一个 "magic keyword"：Word 是用户在 prose 中写下的触发词，
// Directive 是命中后需要注入的隐藏指令（英文、简短，会进入 system/user 提示）。
type MagicKeyword struct {
	Word      string
	Directive string
}

// defaultMagicKeywords 返回默认关键词的有序定义。
// 每次调用都构造新切片，不共享可变全局状态，从而让 MagicKeywordDirective 保持纯函数：
// 调用方即使改写 DefaultMagicKeywords，也不会改变扫描结果。
func defaultMagicKeywords() []MagicKeyword {
	return []MagicKeyword{
		{Word: "ultrathink", Directive: "Think harder: explore the problem thoroughly and reason carefully before answering."},
		{Word: "chillax", Directive: "Keep it brief: answer concisely and quickly."},
	}
}

// DefaultMagicKeywords 是默认 magic keyword 列表（有序）。
// 顺序决定多个关键词同时命中时隐藏指令的拼接顺序。
var DefaultMagicKeywords = defaultMagicKeywords()

// MagicKeywordDirective 扫描用户输入，返回需要注入的隐藏指令文本；无命中返回空串。
//
// 匹配规则：
//   - 大小写不敏感，按 rune 计算单词边界，避免 ultrathinking 之类的误命中；
//   - 忽略围栏代码块（``` / ~~~）、行内代码（`...`）以及形似路径的 token（含 /、\ 或扩展名）；
//   - 只在 prose 中生效；多个不同关键词命中时按 DefaultMagicKeywords 顺序拼接，去重，每个指令一行。
//
// 该函数是纯函数：不读写可变全局状态，不修改入参，输出只由 prompt 决定。
func MagicKeywordDirective(prompt string) string {
	if prompt == "" {
		return ""
	}
	runes := []rune(prompt)
	codeMask := magicCodeMask(runes)
	specs := defaultMagicKeywords()

	matched := make(map[string]bool, len(specs))
	for i := 0; i < len(runes); i++ {
		if codeMask[i] {
			continue
		}
		for _, spec := range specs {
			word := []rune(spec.Word)
			end := i + len(word)
			if end > len(runes) || !magicRegionProse(codeMask, i, end) {
				continue
			}
			if !magicWordEqual(runes[i:end], word) {
				continue
			}
			if !magicBoundaryLeft(runes, codeMask, i) || !magicBoundaryRight(runes, codeMask, end) {
				continue
			}
			matched[spec.Word] = true
		}
	}
	if len(matched) == 0 {
		return ""
	}

	// 按默认列表顺序输出，天然去重且每种关键词只贡献一行指令。
	var b strings.Builder
	for _, spec := range specs {
		if !matched[spec.Word] {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(spec.Directive)
	}
	return b.String()
}

// magicWordEqual 以 ASCII 大小写不敏感的方式比较一段 rune 与关键词是否相等。
// 关键词均为 ASCII 小写，逐 rune 比较避免CJK等多字节字符被字节切分。
func magicWordEqual(got, word []rune) bool {
	if len(got) != len(word) {
		return false
	}
	for i, want := range word {
		r := got[i]
		if r >= 'A' && r <= 'Z' {
			r += 'a' - 'A'
		}
		if r != want {
			return false
		}
	}
	return true
}

// magicRegionProse 检查 [start,end) 整段是否都在 prose 中（未被围栏或行内代码遮蔽）。
func magicRegionProse(mask []bool, start, end int) bool {
	for i := start; i < end; i++ {
		if mask[i] {
			return false
		}
	}
	return true
}

// isMagicSeparator 判断 rune 是否可充当 prose 边界。
// 空白与常见中英文句读算边界；字母、数字、下划线以及 / \ . : ( ) 等
// 与路径、扩展名、符号引用、调用语法相关的字符不算边界。
func isMagicSeparator(r rune) bool {
	if unicode.IsSpace(r) {
		return true
	}
	switch r {
	case ',', ';', '!', '?', '"', '\'', '“', '”', '‘', '’',
		'，', '；', '！', '？', '、', '。', '：':
		return true
	}
	return false
}

// magicBoundaryLeft 检查关键词首字符左侧是否可作为边界。
func magicBoundaryLeft(runes []rune, mask []bool, pos int) bool {
	if pos <= 0 || mask[pos-1] {
		return true
	}
	r := runes[pos-1]
	if r == '.' {
		// 仅当 ASCII 句点前是空白或行首时才视为句读，否则视为扩展名/符号引用的一部分。
		return pos-2 < 0 || unicode.IsSpace(runes[pos-2])
	}
	return isMagicSeparator(r)
}

// magicBoundaryRight 检查关键词末字符右侧是否可作为边界。
func magicBoundaryRight(runes []rune, mask []bool, pos int) bool {
	if pos >= len(runes) || mask[pos] {
		return true
	}
	r := runes[pos]
	if r == '.' {
		// 句点后是空白或结尾才算句读；紧跟字母则视为 .go/.ts/.py 之类的扩展名。
		return pos+1 >= len(runes) || unicode.IsSpace(runes[pos+1])
	}
	return isMagicSeparator(r)
}

// magicCodeMask 标记哪些 rune 位于围栏代码块或行内代码中（含界定符本身）。
// 先识别围栏再识别行内代码，避免 ``` 被误当作行内代码界定符。
func magicCodeMask(runes []rune) []bool {
	mask := make([]bool, len(runes))
	inFence := false
	fenceChar := rune(0)
	fenceLen := 0

	for lineStart := 0; lineStart < len(runes); {
		lineEnd := lineStart
		for lineEnd < len(runes) && runes[lineEnd] != '\n' {
			lineEnd++
		}
		p := lineStart
		for p < lineEnd && (runes[p] == ' ' || runes[p] == '\t' || runes[p] == '\r') {
			p++
		}
		fence, flen := magicFenceAt(runes, p, lineEnd)

		switch {
		case !inFence && fence != 0:
			inFence, fenceChar, fenceLen = true, fence, flen
			magicMaskLine(mask, lineStart, lineEnd)
		case inFence:
			magicMaskLine(mask, lineStart, lineEnd)
			if fence == fenceChar && flen >= fenceLen && magicOnlySpace(runes, p+flen, lineEnd) {
				inFence, fenceChar, fenceLen = false, 0, 0
			}
		default:
			magicMaskInlineCode(mask, runes, lineStart, lineEnd)
		}

		lineStart = lineEnd
		if lineStart < len(runes) {
			lineStart++ // 跳过换行符
		}
	}
	return mask
}

// magicFenceAt 识别行首的围栏标记，返回围栏字符与长度（0 表示不是围栏行）。
func magicFenceAt(runes []rune, p, lineEnd int) (rune, int) {
	if p >= lineEnd || (runes[p] != '`' && runes[p] != '~') {
		return 0, 0
	}
	ch := runes[p]
	n := 0
	for p+n < lineEnd && runes[p+n] == ch {
		n++
	}
	if n < 3 {
		return 0, 0
	}
	return ch, n
}

// magicOnlySpace 检查 [start,end) 是否只含空白，用于判断围栏闭合行没有多余内容。
func magicOnlySpace(runes []rune, start, end int) bool {
	for i := start; i < end; i++ {
		if !unicode.IsSpace(runes[i]) {
			return false
		}
	}
	return true
}

// magicMaskLine 把一整行标记为代码。
func magicMaskLine(mask []bool, start, end int) {
	for i := start; i < end; i++ {
		mask[i] = true
	}
}

// magicMaskInlineCode 标记单行内的行内代码跨度（`...`，含界定符）。
// 未闭合的反引号只标记自身，避免把后续 prose 误判为代码。
func magicMaskInlineCode(mask []bool, runes []rune, lineStart, lineEnd int) {
	for i := lineStart; i < lineEnd; i++ {
		if runes[i] != '`' || mask[i] {
			continue
		}
		close := -1
		for j := i + 1; j < lineEnd; j++ {
			if runes[j] == '`' {
				close = j
				break
			}
		}
		if close < 0 {
			mask[i] = true
			continue
		}
		for j := i; j <= close; j++ {
			mask[j] = true
		}
		i = close
	}
}
