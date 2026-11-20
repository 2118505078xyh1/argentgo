package pty

// stripTerminalQueries 从一块 PTY 输出中移除终端查询/报告转义序列，
// stripTerminalQueries 从一块 PTY 输出中移除终端查询/报告转义序列，返回干净字节以及必须前置到下一块再传给此处的任何尾部不完整转义序列。
// 必须前置到下一块再传给此处的任何尾部不完整
// 转义序列。
//
// 它存在的原因：gh/npm 这样的 CLI —— 更糟的是攻击者控制的工具
// 输出 —— 会发出要求终端报告自身状态的序列：
//
//   - DSR：ESC [ 6 n（及 ESC [ 5 n）
//
//   - DA / DA2：ESC [ c（及 ESC [ > c）
//   - DECRQSS：ESC [ $ q
//
// 真实终端通过向字节流注入*更多*转义序列来回答每一个。
// 在 argentgo 中该流由 TUI/native 前端渲染并回显给模型，因此回答成为
// 提示注入通道。合法的显示序列（SGR 颜色/粗体、光标移动、
// 字符集指示符、OSC 超链接/标题）逐字节保留。
// 过滤器面向字节且只曾扣留 ASCII 转义字节，因此
//
// 跨块边界拆分的 UTF-8 rune 被完整发出；保持流良好
// 该过滤器面向字节且只曾扣留 ASCII 转义字节，因此跨块边界拆分的 UTF-8 rune 被完整发出；保持流良好形式是调用方的 UTF-8 进位写入器的职责，而非此处。复制而非别名：调用方复用其 Read 缓冲区，因此待处理字节不得指向 chunk 的底层数组。
// 形式是调用方的 UTF-8 进位写入器的职责，而非此处。
func stripTerminalQueries(chunk []byte) (clean []byte, pending []byte) {
	clean = make([]byte, 0, len(chunk))
	for i := 0; i < len(chunk); {
		if chunk[i] != 0x1b { // ESC — the only byte that can start a sequence
			clean = append(clean, chunk[i])
			i++
			continue
		}
		end, query, incomplete := scanEscape(chunk, i)
		if incomplete {
			// 复制而非别名：调用方复用其 Read 缓冲区，因此
			// 待处理字节不得指向 chunk 的底层数组。
			pending = append(pending, chunk[i:]...)
			break
		}
		if !query {
			clean = append(clean, chunk[i:end]...)
		}
		i = end
	}
	return clean, pending
}

// scanEscape 解析从 chunk[i]（必须是 ESC）开始的转义序列。它返回刚好越过该序列的索引、它是否是应丢弃的终端查询，以及该序列在此块中是否不完整必须扣留。
// ESC）。它返回刚好越过该序列的索引、它是否是终端
// 查询应被丢弃，以及该序列在此块中是否不完整
// 必须扣留。
func scanEscape(chunk []byte, i int) (end int, query bool, incomplete bool) {
	if i+1 >= len(chunk) {
		return 0, false, true // lone ESC: wait for the introducer
	}
	switch chunk[i+1] {
	case '[': // CSI
		return scanCSI(chunk, i+2)
	case ']': // OSC
		return scanOSC(chunk, i+2)
	case 'P', 'X', '^', '_': // DCS, SOS, PM, APC: string terminated by ST
		end, incomplete = scanStringEscape(chunk, i+2)
		return end, false, incomplete
	case '(', ')', '*', '+', '-', '.', '/': // charset designation: one byte
		if i+2 >= len(chunk) {
			return 0, false, true
		}
		return i + 3, false, false
	default: // generic escape: ESC + one byte (RIS, save/restore cursor, ...)
		return i + 2, false, false
	}
}

// scanCSI 解析主体从 body（紧跟 ESC '['）开始的 CSI。消耗直到（不含）该字节并让调用方在其上重新同步，而非吞掉无关文本。
func scanCSI(chunk []byte, body int) (end int, query bool, incomplete bool) {
	j := body
	for j < len(chunk) && chunk[j] >= 0x30 && chunk[j] <= 0x3f { // parameter bytes
		j++
	}
	intermediates := j
	for j < len(chunk) && chunk[j] >= 0x20 && chunk[j] <= 0x2f { // intermediate bytes
		j++
	}
	if j >= len(chunk) {
		return 0, false, true
	}
	final := chunk[j]
	if final < 0x40 || final > 0x7e { // not a well-formed final byte
		// 消耗直到（不含）该字节并让调用方
		// 在其上重新同步，而非吞掉无关文本。
		return j, false, false
	}
	switch final {
	case 'n', 'c': // DSR, DA / DA2
		query = true
	case 'q': // DECRQSS is distinguished from setters such as DECSCA by '$'
		for k := intermediates; k < j; k++ {
			if chunk[k] == '$' {
				query = true
			}
		}
	}
	return j + 1, query, false
}

// scanOSC 解析主体从 body（紧跟 ESC ']'）开始的 OSC。当
// scanOSC 解析主体从 body（紧跟 ESC ']'）开始的 OSC。当其 payload 包含 ";?" 模式时它是查询（OSC 10/11/12/4/52 查询）。ST 是 ESC '\'；BEL 也终止 OSC。
// 查询）。ST 是 ESC '\'；BEL 也终止 OSC。
func scanOSC(chunk []byte, body int) (end int, query bool, incomplete bool) {
	j := body
	var prev byte
	for j < len(chunk) {
		c := chunk[j]
		if c == 0x07 { // BEL
			return j + 1, query, false
		}
		if c == 0x1b {
			if j+1 >= len(chunk) {
				return 0, false, true // the next byte may be '\' (ST)
			}
			if chunk[j+1] == '\\' {
				return j + 2, query, false
			}
		}
		if c == '?' && prev == ';' {
			query = true
		}
		prev = c
		j++
	}
	return 0, false, true // no terminator yet
}

// scanStringEscape 解析 DCS/SOS/PM/APC 主体（在其引导符之后）直到字符串终止符。它们从不是查询，但仍必须跟踪，使跨块拆分的终止符不被撕开。
// 字符串终止符。它们从不是查询，但仍必须跟踪，使
// 跨块拆分的终止符不被撕开。
func scanStringEscape(chunk []byte, body int) (end int, incomplete bool) {
	for j := body; j < len(chunk); j++ {
		if chunk[j] == 0x07 { // some endpoints accept BEL here too
			return j + 1, false
		}
		if chunk[j] == 0x1b {
			if j+1 >= len(chunk) {
				return 0, true
			}
			if chunk[j+1] == '\\' { // ST
				return j + 2, false
			}
		}
	}
	return 0, true
}

// queryFilter 是 stripTerminalQueries 的流式包装器：它在 Write 调用之间保留
// 尾部不完整序列，使调用方可以把重复 Read 调用的块喂给它
// 而无需自己管理待处理字节。它不可并发
// queryFilter 是 stripTerminalQueries 的流式包装器：它在 Write 调用之间保留尾部不完整序列，使调用方可以把重复 Read 调用的块喂给它而无需自己管理待处理字节。它不可并发使用 —— 每条 PTY 流只有一个读者 goroutine。
type queryFilter struct {
	pending []byte
}

// Write 过滤一块并只返回现在可安全发出的字节。
func (f *queryFilter) Write(chunk []byte) []byte {
	data := chunk
	if len(f.pending) > 0 {
		data = make([]byte, 0, len(f.pending)+len(chunk))
		data = append(data, f.pending...)
		data = append(data, chunk...)
		f.pending = f.pending[:0]
	}
	clean, pending := stripTerminalQueries(data)
	f.pending = append(f.pending[:0], pending...)
	return clean
}

// Write 过滤一块并只返回现在可安全发出的字节。Flush 在 EOF 发出任何保留的不完整序列。悬空片段不可能是完整查询，因此它被原样写出。Flush 是幂等的。
// 能是完整查询，因此它被原样写出。Flush 是幂等的。
func (f *queryFilter) Flush() []byte {
	out := f.pending
	f.pending = nil
	return out
}
