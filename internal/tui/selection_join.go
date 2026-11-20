package tui

import "strings"

// codeSpanMark 是 SelectedText 在行内代码段两侧写入的内部定界标记。
// 屏幕单元格只保存去掉反引号的代码文本，复制时需要借这个标记把定界符补回来；
// 普通终端文本不会出现 NUL，所以不会和真实内容冲突。
const codeSpanMark = "\x00"

// joinSelectedRows 把逐屏行拼回一段便于粘贴的纯文本。
//
// 换行策略：
//   - 行宽（按显示单元格计）达到视口宽的 3/5 时视为软折行，和下一行用空格相接；
//   - 空行（段落间的空白屏行）保留为段落分隔，产生换行；
//   - 其余情况按原屏幕行换行。
//
// 行宽用 TextWidth 计算，按字素簇而非字节计数，因此 CJK、组 emoji 都不会在
// 边界处被拆坏。SelectedText 写入的 codeSpanMark 会被还原成反引号，跨行的
// 代码段也能在拼接后正确配对。
func joinSelectedRows(rows []string, viewportWidth int) string {
	if len(rows) == 0 {
		return ""
	}
	threshold := viewportWidth * 3 / 5
	var out strings.Builder
	for i, row := range rows {
		out.WriteString(row)
		if i == len(rows)-1 {
			break
		}
		sep := "\n"
		// 相邻两行都非空、且当前行接近占满视口，才判定为软折行接空格。
		// 空行意味着段落结束，无论上一行多宽都用换行分隔。
		if row != "" && rows[i+1] != "" && threshold > 0 && TextWidth(row) >= threshold {
			sep = " "
		}
		out.WriteString(sep)
	}
	return restoreCodeSpans(out.String())
}

// restoreCodeSpans 把成对的 codeSpanMark 还原成反引号，落单的标记直接丢弃。
// 在拼好的整段文本上做一次全局替换，这样被软折行切开的代码段也能配对。
func restoreCodeSpans(s string) string {
	if !strings.Contains(s, codeSpanMark) {
		return s
	}
	parts := strings.Split(s, codeSpanMark)
	var out strings.Builder
	for i, part := range parts {
		// 奇数段位于两个标记之间，配成对时补上反引号。
		if i%2 == 1 && i < len(parts)-1 {
			out.WriteByte('`')
			out.WriteString(part)
			out.WriteByte('`')
			continue
		}
		out.WriteString(part)
	}
	return out.String()
}
