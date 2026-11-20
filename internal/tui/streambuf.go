package tui

import "strings"

// MarkdownStreamBuf 暂时保留流式助手回复的尾部，直到
// 它可能开启的 markdown 结构完成。增量可以在任意字节处
// 拆开 `**`、反引号或 ``` 围栏；渲染这样的前缀会让
// 标记短暂出现又消失，产生闪烁。Push 返回
// 可安全渲染的前缀，其余留给下一个增量。
// Flush 在回合结束时释放剩余部分。
//
// 除被保留的文本外，缓冲区刻意无状态：每次
// Push 都从待处理字节重新推导围栏/行内代码/强调状态，
// 因此切割只能落在没有结构打开的位置。
type MarkdownStreamBuf struct {
	pending string
}

// Push 添加一个增量，返回现在可安全渲染的部分。普通
// 文本直接通过；只有可能的标记前缀被保留。
func (b *MarkdownStreamBuf) Push(delta string) string {
	if delta == "" {
		return ""
	}
	b.pending += delta
	cut := commitFrontier(b.pending)
	if cut == 0 {
		return ""
	}
	out := b.pending[:cut]
	b.pending = b.pending[cut:]
	return out
}

// Flush 释放所有被保留的字节，例如在回合结束时。
func (b *MarkdownStreamBuf) Flush() string {
	out := b.pending
	b.pending = ""
	return out
}

// Reset 丢弃任何被保留的尾部而不输出它。
func (b *MarkdownStreamBuf) Reset() {
	b.pending = ""
}

func isMarkdownMarker(c rune) bool {
	return c == '*' || c == '_' || c == '`'
}

// commitFrontier 返回 s 中第一个不完整的 markdown 结构之前的
// 字节索引。该索引之前的所有内容渲染时不会闪烁。
func commitFrontier(s string) int {
	r := []rune(s)
	n := len(r)
	if n == 0 {
		return 0
	}
	bol := 0 // rune index of the current line's first character
	i := 0
	// advance 把 i 向前移动，并保持 bol 与跨过的换行同步。
	advance := func(to int) {
		for ; i < to; i++ {
			if r[i] == '\n' {
				bol = i + 1
			}
		}
	}
	consumed := 0 // rune index just past the last fully consumed construct
	for i < n {
		// 围栏是行首（跳过可选空白后）连续 >=3 个反引号，
		// 与 markdownLines 的 TrimSpace + HasPrefix 一致。
		if lineStartWhitespace(r, bol, i) {
			k := i
			for k < n && (r[k] == ' ' || r[k] == '\t') {
				k++
			}
			if k < n && r[k] == '`' {
				if run := backtickRun(r, k); run >= 3 {
					if close := findFenceClose(r, k+run); close >= 0 {
						advance(close)
						consumed = i
						continue
					}
					// 围栏已打开但未闭合：从其分隔符开始保留。
					return runeByteOffset(r, k)
				}
			}
		}
		// 表格是块级结构：markdownLines 一次渲染整个
		// 块，因为列宽取决于每一行。提交部分表格会
		// 在后续行到达时把早期行重新渲染成新的正文行
		// ，因此保留它直到更后面的行证明块已结束。
		if i == bol {
			if commit, hold, ok := tableBlockFrontier(r, i); ok {
				if hold {
					return runeByteOffset(r, i)
				}
				advance(commit)
				consumed = i
				continue
			}
		}
		switch r[i] {
		case '`':
			if end := findInlineClose(r, i); end >= 0 {
				advance(end + 1)
				consumed = i
				continue
			}
			return runeByteOffset(r, i) // inline code not closed yet
		case '*':
			if i+1 < n && r[i+1] == '*' {
				if end := findPairClose(r, i+2, '*'); end >= 0 {
					advance(end + 2)
					consumed = i
					continue
				}
				return runeByteOffset(r, i) // bold not closed yet
			}
		case '_':
			if i+1 < n && r[i+1] == '_' {
				if end := findPairClose(r, i+2, '_'); end >= 0 {
					advance(end + 2)
					consumed = i
					continue
				}
				return runeByteOffset(r, i) // underscore emphasis not closed yet
			}
		}
		i++
		if i < n && r[i-1] == '\n' {
			bol = i
		}
	}
	// 没有结构打开。仍保留尾部连续的一或两个标记
	// 字符：下一个增量到达时，它可能是 `**`、行内代码段或
	// 围栏的开头。连续 >=3 个已经是完整的
	// 围栏标记，因此可以提交。
	start := trailingMarkerRun(r)
	if start < n && n-start <= 2 && start >= consumed {
		return runeByteOffset(r, start)
	}
	return len(s)
}

// lineStartWhitespace 报告 r[bol:i] 是否为空或只有空格/制表符，
// 即 i 是其所在行的第一个非空白 rune。
func lineStartWhitespace(r []rune, bol, i int) bool {
	for j := bol; j < i; j++ {
		if r[j] != ' ' && r[j] != '\t' {
			return false
		}
	}
	return true
}

func backtickRun(r []rune, i int) int {
	j := i
	for j < len(r) && r[j] == '`' {
		j++
	}
	return j - i
}

// findFenceClose 返回下一行开头连续 >=3 个反引号之后的
// rune 索引，即围栏闭合符。扫描从 from 的
// 下一行开始，与渲染器逐行切换的方式一致。
func findFenceClose(r []rune, from int) int {
	n := len(r)
	i := from
	for i < n && r[i] != '\n' {
		i++
	}
	if i < n {
		i++
	}
	for i < n {
		k := i
		for k < n && (r[k] == ' ' || r[k] == '\t') {
			k++
		}
		if k < n && r[k] == '`' {
			if run := backtickRun(r, k); run >= 3 {
				return k + run
			}
		}
		for i < n && r[i] != '\n' {
			i++
		}
		if i < n {
			i++
		}
	}
	return -1
}

// findInlineClose 查找闭合反引号，其与 from 处开启符之间
// 至少有一个内容 rune，与 parseInline 一致。
func findInlineClose(r []rune, from int) int {
	for j := from + 2; j < len(r); j++ {
		if r[j] == '`' {
			return j
		}
	}
	return -1
}

// findPairClose 查找 from 之后至少有一个内容 rune 的
// 闭合 ** 或 __ 对，与 parseInline 的粗体分支一致。
func findPairClose(r []rune, from int, mark rune) int {
	for j := from + 1; j+1 < len(r); j++ {
		if r[j] == mark && r[j+1] == mark {
			return j
		}
	}
	return -1
}

func trailingMarkerRun(r []rune) int {
	i := len(r)
	for i > 0 && isMarkdownMarker(r[i-1]) {
		i--
	}
	return i
}

// runeByteOffset 把 rune 索引转换为字节索引，使切割永不拆分
// UTF-8 序列，包括 CJK 和 emoji。
func runeByteOffset(r []rune, idx int) int {
	return len(string(r[:idx]))
}

// lineEnd 返回结束 i 所在行的换行符的 rune 索引，若该行是
// 最后一行则返回 len(r)。它镜像 markdownLines 在测试表格前
// 所依据的原始行切分。
func lineEnd(r []rune, i int) int {
	for i < len(r) && r[i] != '\n' {
		i++
	}
	return i
}

// tableBlockFrontier 检查从 start 开始的行是否为 markdown 表格，
// 并返回渲染可安全恢复的位置。含 '|' 的行是
// 潜在的表头，直到下一行完整并排除
// 分隔符行；在那之前必须保留它，否则原始表头会
// 先显示，再在分隔符到达时被替换为对齐的表格。
// ok=false 表示这里没有需要保留的内容（没有 '|'，或该行是
// 已解析的普通竖线行）。hold=true 表示从 start 开始保留；
// hold=false 在 commit 中返回刚过完整块的 rune 索引。
//
// markdownLines 在第一个不含 '|' 的非空白行处判定块结束。
// 等待该行使已显示的每一行的列宽保持稳定，
// 因为每个正文行都可能改变每一列的宽度。
func tableBlockFrontier(r []rune, start int) (commit int, hold, ok bool) {
	headerEnd := lineEnd(r, start)
	if !strings.Contains(string(r[start:headerEnd]), "|") {
		return 0, false, false
	}
	if headerEnd >= len(r) {
		return 0, true, true // no following line yet: could be a header
	}
	delim := headerEnd + 1
	delimEnd := lineEnd(r, delim)
	if delimEnd >= len(r) {
		return 0, true, true // delimiter line still streaming, undecidable
	}
	if _, valid := parseTableDelimiter(string(r[delim:delimEnd])); !valid {
		return 0, false, false // ordinary pipe line, safe to commit
	}
	for pos := delimEnd + 1; pos < len(r); {
		end := lineEnd(r, pos)
		if strings.Contains(string(r[pos:end]), "|") {
			pos = end + 1
			continue
		}
		// 只有完整的行才能结束块。未终结的行可能
		// 在下一个增量中长出一个 '|' 并成为表格的一部分。
		if end < len(r) {
			return pos, false, true
		}
		return 0, true, true
	}
	return 0, true, true
}
