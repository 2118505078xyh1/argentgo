package tools

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
)

// 搜索扫描的内存上限。这些常量来自 oh-my-pi xfs 的实测口径：
// 小文件一次读入避免逐行分配，大文件流式处理避免整文件驻留，
// 单行超限时整行丢弃（既不匹配也不保留），前置窗口只做 NUL 检测。
const (
	// searchScanMaxFileBytes 是"小文件"阈值。不超过该大小时一次性读入，
	// 不做 os.ReadFile 的 stat + 双拷，也不做 strings.Split 的全量切片拷贝。
	searchScanMaxFileBytes = 4 << 20 // 4 MiB
	// searchScanBinaryWindow 是前置 NUL 检测窗口。
	searchScanBinaryWindow = 64 << 10 // 64 KiB
	// searchScanLineLimitBytes 是单行上限；超过该字节数的行整行跳过。
	searchScanLineLimitBytes = 64 << 20 // 64 MiB
	// searchScanMaxColumns 是输出到模型前的列（rune）上限，防止超长行
	// 吞掉整个输出预算；与 read 工具的按 rune 截断语义一致。
	searchScanMaxColumns = 2000
)

// scanLine 是一次扫描产出的行。text 仅在 visit 回调期间有效：
// 小文件模式下它是读入缓冲区的切片，流式模式下它是复用的 scratch。
// 调用方若需保留，必须显式拷贝为 string。
type scanLine struct {
	no   int    // 1-based 行号
	text []byte // 已归一 CRLF，未做列截断
}

// scanStats 描述一次扫描的副作用，供上层生成截断/跳过提示。
type scanStats struct {
	binary     bool  // 前置窗口含 NUL，文件被判为二进制并跳过
	linesTotal int   // 实际回调过的行数（含被跳过的长行的行号占位）
	longLines  int   // 因超过 searchScanLineLimitBytes 而被跳过的行数
	bytes      int64 // 文件字节数（stat 可得时）
}

// scanFileLines 扫描 path 的每一行并调用 visit；visit 返回 false 时提前停止。
// 它按文件大小选择单次读入或流式分块，两种路径的行语义完全一致。
func scanSearchFileLines(path string, visit func(scanLine) bool) scanStats {
	var st scanStats
	f, err := os.Open(path)
	if err != nil {
		return st
	}
	defer f.Close()

	info, statErr := f.Stat()
	if statErr == nil {
		if !info.Mode().IsRegular() {
			return st
		}
		st.bytes = info.Size()
	}

	if statErr == nil && info.Size() <= searchScanMaxFileBytes {
		scanSmallFile(f, int(info.Size()), visit, &st)
		return st
	}
	scanLargeFile(f, visit, &st)
	return st
}

// scanSmallFile 对已知大小的"小文件"做一次读入后立即按 '\n' 切片。
// 不构造中间字符串，也不分配行切片数组。
func scanSmallFile(f *os.File, size int, visit func(scanLine) bool, st *scanStats) {
	data := make([]byte, size)
	if _, err := io.ReadFull(f, data); err != nil && !errors.Is(err, io.EOF) {
		return
	}
	window := data
	if len(window) > searchScanBinaryWindow {
		window = window[:searchScanBinaryWindow]
	}
	if bytes.IndexByte(window, 0) >= 0 {
		st.binary = true
		return
	}

	lineNo := 0
	for start := 0; start < len(data); {
		idx := bytes.IndexByte(data[start:], '\n')
		end := len(data)
		hasNL := idx >= 0
		if hasNL {
			end = start + idx
		}
		seg := data[start:end]
		lineNo++
		if len(seg) > searchScanLineLimitBytes {
			st.longLines++
		} else {
			if n := len(seg); n > 0 && seg[n-1] == '\r' {
				seg = seg[:n-1]
			}
			st.linesTotal++
			if !visit(scanLine{no: lineNo, text: seg}) {
				return
			}
		}
		if !hasNL {
			break
		}
		start = end + 1
	}
}

// scanLargeFile 对"大文件"先做 64 KiB 前置 NUL 检测，再用 bufio.Reader
// 分块扫描。行缓冲可复用，单个超长行只占用上限内的 scratch，不会因
// 一行巨行而分配 64 MiB 以上的常驻内存。
func scanLargeFile(f *os.File, visit func(scanLine) bool, st *scanStats) {
	br := bufio.NewReaderSize(f, searchScanBinaryWindow)
	head, _ := br.Peek(searchScanBinaryWindow)
	if bytes.IndexByte(head, 0) >= 0 {
		st.binary = true
		return
	}

	lineNo := 0
	var lineBuf []byte
	skipping := false
	for {
		frag, err := br.ReadSlice('\n')
		if len(frag) > 0 && !skipping {
			if len(lineBuf)+len(frag) > searchScanLineLimitBytes {
				skipping = true
				st.longLines++
				lineBuf = lineBuf[:0]
			} else {
				lineBuf = append(lineBuf, frag...)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return
		}

		// 只有读到分隔符（err==nil）或 EOF 且尚有残片时才构成一行。
		if err == nil || (errors.Is(err, io.EOF) && len(frag) > 0) {
			lineNo++
			if !skipping {
				line := lineBuf
				if n := len(line); n > 0 && line[n-1] == '\n' {
					line = line[:n-1]
				}
				if n := len(line); n > 0 && line[n-1] == '\r' {
					line = line[:n-1]
				}
				st.linesTotal++
				if !visit(scanLine{no: lineNo, text: line}) {
					return
				}
			}
			lineBuf = lineBuf[:0]
			skipping = false
		}
		if errors.Is(err, io.EOF) {
			return
		}
	}
}

// lineText 把扫描行转成可保留的字符串，并按列上限安全截断（rune 对齐）。
// CJK / emoji 不会被切坏；未超限时原样返回。
func lineText(line []byte) string {
	return capLineRunes(string(line), searchScanMaxColumns)
}

// 上下文行的种类。
const (
	contextBefore byte = iota // 匹配前一行
	contextMatch              // 命中行
	contextAfter              // 匹配后一行
)

// contextBuffer 在流式扫描中维护 before/after 上下文。它只保留最近
// before 行的拷贝，并在 after 窗口内把后续行投递给 onLine，从而无需
// 把整个文件驻留内存就能产出与整文件读取完全一致的上下文。
type contextBuffer struct {
	before    int
	after     int
	pending   []string
	afterLeft int
	onLine    func(kind byte, lineNo int, text string)
}

func newContextBuffer(before, after int, onLine func(kind byte, lineNo int, text string)) *contextBuffer {
	return &contextBuffer{before: before, after: after, onLine: onLine}
}

// push 把一行送入上下文状态机。toText 只在需要保留该行时才被调用，
// 避免为无上下文的普通行分配字符串。
func (cb *contextBuffer) push(lineNo int, line []byte, matched bool, toText func([]byte) string) {
	if matched {
		// 先投递命中行，消费方记录其下标；随后投递的前置行才能挂到
		// 这一次命中上（而非上一次命中）。
		cb.onLine(contextMatch, lineNo, toText(line))
		for i, l := range cb.pending {
			cb.onLine(contextBefore, lineNo-len(cb.pending)+i, l)
		}
		cb.afterLeft = cb.after
	} else if cb.afterLeft > 0 {
		cb.onLine(contextAfter, lineNo, toText(line))
		cb.afterLeft--
	}
	if cb.before > 0 {
		cb.pending = append(cb.pending, toText(line))
		if len(cb.pending) > cb.before {
			copy(cb.pending, cb.pending[len(cb.pending)-cb.before:])
			cb.pending = cb.pending[:cb.before]
		}
	}
}
