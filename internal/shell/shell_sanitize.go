package shell

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// sanitizeShellOutput 让原始 shell 输出可以安全地交给模型。
//
// 处理流水线：完整消费 ANSI 转义序列（只丢弃 ESC，避免像 vite 这样的
// 彩色日志中留下字面上的 "[36m" 垃圾），移除其他
// C0 控制字符，移除行间注释字符。
// 随后按终端渲染的方式规范化幸存文本：
// 回车会覆盖行内容，因此只保留最后一个进度帧；
// 退格会破坏性地擦除前一个字符；连续
// 相同的行会被折叠并加标记。包括 CJK 在内的 rune 绝不会
// 在 UTF-8 边界处被拆分。
func sanitizeShellOutput(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		switch c {
		case 0x1b: // ESC starts an escape sequence, never a printable character.
			i = skipShellEscapeSequence(s, i+1)
			continue
		case '\t', '\n', '\r', '\b':
			b.WriteByte(c)
			i++
			continue
		}
		if c < 0x20 { // remaining C0 controls -> drop
			i++
			continue
		}
		if c < utf8.RuneSelf {
			b.WriteByte(c)
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if size == 0 { // defensive: non-empty input always decodes at least one byte
			size = 1
		}
		if r >= 0xfff9 && r <= 0xfffb { // interlinear annotation -> drop
			i += size
			continue
		}
		// 保留原始字节，包括无效的 UTF-8 字节，而不是
		// 替换成某个 rune 从而破坏周围文本。
		b.WriteString(s[i : i+size])
		i += size
	}
	// 在解释覆盖之前先剥离转义正文：ANSI 序列在真实终端上不占
	// 任何列，因此把它们计入会导致光标错位并破坏进度帧。终端风格的
	// 处理只在清理后的文本上运行，
	// 最后再压缩重复行。
	text := b.String()
	text = processCarriageReturns(text)
	text = processBackspaces(text)
	return applyRunLengthEncoding(text)
}

// processCarriageReturns 模拟真实终端：在每行内，回车
// 把光标移回第 0 列，其后的 rune 覆盖
// 已有内容。这样只留下进度显示的最后一帧。
//
// Cline 的参考实现基于 UTF-16 码元工作，并在部分覆盖拆分
// 代理对时补一个空格。这里的覆盖按完整 rune 进行，因此多字节字符
// 会作为一个整体被替换，永远不会被拆分 ——
// 无需补空格，CJK/emoji 保持完好。
func processCarriageReturns(s string) string {
	if !strings.ContainsRune(s, '\r') {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if strings.ContainsRune(line, '\r') {
			lines[i] = processCarriageReturnLine(line)
		}
	}
	return strings.Join(lines, "\n")
}

func processCarriageReturnLine(line string) string {
	out := make([]rune, 0, utf8.RuneCountInString(line))
	cursor := 0
	for _, r := range line {
		if r == '\r' {
			cursor = 0
			continue
		}
		if cursor < len(out) {
			out[cursor] = r
		} else {
			out = append(out, r)
		}
		cursor++
	}
	return string(out)
}

// processBackspaces 把退格视为对其前一个 rune 的破坏性删除，
// 与 Cline/Roo-Code 用于进度指示器的终端输出优化一致。开头的
// 退格以及没有内容可擦除的
// 退格都是空操作。
func processBackspaces(s string) string {
	if !strings.ContainsRune(s, '\b') {
		return s
	}
	out := make([]rune, 0, utf8.RuneCountInString(s))
	for _, r := range s {
		if r == '\b' {
			if len(out) > 0 {
				out = out[:len(out)-1]
			}
			continue
		}
		out = append(out, r)
	}
	return string(out)
}

// applyRunLengthEncoding 把连续相同的行折叠为第一行
// 加上一个"<前一行额外重复了 N 次>"标记。仅当压缩形式比原始
// 连续行更短时才使用该标记；重复的短行
// 保持不变。
func applyRunLengthEncoding(s string) string {
	if s == "" {
		return s
	}
	var out strings.Builder
	out.Grow(len(s))

	prevLine := ""
	hasPrev := false
	repeat := 0

	flush := func() {
		if !hasPrev {
			return
		}
		if repeat == 0 {
			out.WriteString(prevLine)
		} else {
			desc := fmt.Sprintf("<previous line repeated %d additional times>\n", repeat)
			lineLen := utf8.RuneCountInString(prevLine)
			if len(desc) < lineLen*repeat {
				out.WriteString(prevLine)
				out.WriteString(desc)
			} else {
				for i := 0; i <= repeat; i++ {
					out.WriteString(prevLine)
				}
			}
		}
		repeat = 0
	}

	for s != "" {
		idx := strings.IndexByte(s, '\n')
		var line string
		if idx < 0 {
			line = s
			s = ""
		} else {
			line = s[:idx+1]
			s = s[idx+1:]
		}
		if !hasPrev {
			prevLine = line
			hasPrev = true
		} else if line == prevLine {
			repeat++
		} else {
			flush()
			prevLine = line
		}
	}
	flush()
	return out.String()
}

// skipShellEscapeSequence 消费从 ESC 字节之后开始的一个转义
// 序列正文，并返回紧随其后的第一个字节的索引。它
// 处理 CSI（ESC '[' ... 结束字节 0x40..0.7e）、OSC（ESC ']' ... BEL 或
// ESC '\'）、字符集指定符（ESC '(' X / ESC ')' X），以及吞掉
// 一个后继字符的通用转义。在输入末尾被截断的序列
// 会被消费到末尾，而不是 panic 或泄漏其正文。
func skipShellEscapeSequence(s string, i int) int {
	if i >= len(s) {
		return i
	}
	switch s[i] {
	case '[': // CSI: parameter/intermediate bytes, final byte 0x40..0x7e.
		i++
		for i < len(s) {
			last := s[i]
			i++
			if last >= 0x40 && last <= 0x7e {
				break
			}
		}
	case ']': // OSC: terminated by BEL (0x07) or ST (ESC '\').
		i++
		for i < len(s) {
			c := s[i]
			if c == 0x07 {
				i++
				break
			}
			if c == 0x1b {
				i++
				if i < len(s) && s[i] == '\\' {
					i++
				}
				break
			}
			i++
		}
	case '(', ')': // charset designator: exactly one following byte.
		i++
		if i < len(s) {
			i++
		}
	default: // any other escape swallows one following byte.
		i++
	}
	return i
}
