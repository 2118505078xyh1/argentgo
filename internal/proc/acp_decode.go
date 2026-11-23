package proc

import (
	"io"
	"os"
	"strings"
	"sync"
	"unicode/utf8"
)

// ARGENT_ACP_DECODE 是 UTF-8→ACP 增量解码的 opt-in 开关。
// 仅当取值为 "1"（允许首尾空白）时启用；默认关闭时本包行为逐字节不变。
const acpDecodeEnv = "ARGENT_ACP_DECODE"

// cpUTF8 是 UTF-8 的代码页编号；ACP 本身已是 UTF-8 时无需回退解码。
const cpUTF8 uint32 = 65001

// replacementUTF8 是非法字节按 UTF-8 解码时的标准替换字符。
var replacementUTF8 = []byte("\uFFFD")

// acpDecodeMode 是解码器当前采用的编码。
type acpDecodeMode uint8

const (
	acpModeUTF8 acpDecodeMode = iota
	acpModeACP
)

// acpDecodeEnabled 报告是否启用了 ACP 回退解码。
// 在调用时读取环境，便于测试动态调整；默认关闭。
func acpDecodeEnabled() bool {
	return strings.TrimSpace(os.Getenv(acpDecodeEnv)) == "1"
}

// acpByteDecoder 对一个外部程序的一条输出流做增量编码判定：
// 合法 UTF-8 直通；遇到完整非法序列时切到系统 ACP，用 MultiByteToWideChar
// 解码此后该程序的全部输出直到 EOF（锁存，不再切回）。
//
// 每个 proc.Run 的 stdout/stderr 各持有独立实例，因此一个程序里的杂散字节
// 不会污染另一个程序被捕获的输出；同一条流内已判定的行也不会因回退被改写。
// 它可安全并发使用：exec 的拷贝 goroutine 与 EOF 排空可能分属不同 goroutine。
type acpByteDecoder struct {
	mu       sync.Mutex
	w        io.Writer
	pending  []byte
	mode     acpDecodeMode
	codepage uint32
}

func newACPByteDecoder(w io.Writer) *acpByteDecoder {
	return &acpByteDecoder{w: w, codepage: hostACP()}
}

// canFallback 报告当前 ACP 能否解码 UTF-8 解不了的字节。
// 非 Windows（codepage=0）或系统本身就是 UTF-8 时不做回退。
func (d *acpByteDecoder) canFallback() bool {
	return d.codepage != 0 && d.codepage != cpUTF8
}

// Write 实现 io.Writer：先把 p 并入尚未判定的尾部，再按当前模式排出可安全
// 输出的字节。合法 UTF-8（或 ACP 模式下的纯 ASCII）且无待定字节时走直通
// 快路径，不做额外分配或解码。
func (d *acpByteDecoder) Write(p []byte) (int, error) {
	n := len(p)
	if n == 0 || d == nil {
		return n, nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.w == nil {
		return n, nil
	}
	if len(d.pending) == 0 {
		if d.mode == acpModeUTF8 && utf8.Valid(p) {
			// 快路径：合法 UTF-8 原样转发，零拷贝零解码。
			_, _ = d.w.Write(p)
			return n, nil
		}
		if d.mode == acpModeACP && asciiOnly(p) {
			// ACP 模式下 ASCII 与 UTF-8 同形，直通即可。
			_, _ = d.w.Write(p)
			return n, nil
		}
	}
	d.pending = append(d.pending, p...)
	d.drain(false)
	return n, nil
}

// Finish 在 EOF 排空剩余字节：截断的 UTF-8 尾部会被判定为 ACP 或替换字符。
func (d *acpByteDecoder) Finish() {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.drain(true)
}

func (d *acpByteDecoder) drain(eof bool) {
	if d.mode == acpModeACP {
		d.drainACP(eof)
	} else {
		d.drainUTF8(eof)
	}
}

// drainUTF8 用线性游标扫描待定字节：合法前缀写出；第一个「完整非法序列」
// 触发 ACP 锁存；末尾的不完整序列保留到下次 Write，直到 EOF 才做判定。
// 保留不完整尾部避免了把跨分块的字符误判为非法序列（与 utf8CarryWriter
// 的边界语义一致）。
func (d *acpByteDecoder) drainUTF8(eof bool) {
	b := d.pending
	emit, scan := 0, 0
	hold := len(b) // b[hold:] 是尚未判定、需要留给下一次的尾部
	for scan < len(b) {
		r, size := utf8.DecodeRune(b[scan:])
		if r != utf8.RuneError || size > 1 {
			scan += size
			continue
		}
		// (RuneError, 1) 同时覆盖「完整非法字节」与「末尾被截断的序列」。
		if !utf8.FullRune(b[scan:]) {
			// 截断序列：等待后续字节补全，避免误判。
			if eof && d.canFallback() {
				d.switchToACP(b, emit, true)
				return
			}
			if eof {
				// 无回退：已确定前缀 + 替换字符，尾部不再保留。
				if scan > emit {
					_, _ = d.w.Write(b[emit:scan])
				}
				_, _ = d.w.Write(replacementUTF8)
				emit = len(b)
			} else {
				hold = scan
			}
			break
		}
		// 完整非法字节：UTF-8 已判定失败，永久切到 ACP。
		if d.canFallback() {
			d.switchToACP(b, emit, eof)
			return
		}
		// 无回退（非 Windows / ACP 即 UTF-8）：写替换字符后继续扫描。
		if scan > emit {
			_, _ = d.w.Write(b[emit:scan])
		}
		_, _ = d.w.Write(replacementUTF8)
		emit = scan + 1
		scan = emit
	}
	// 只写出已判定的前缀 b[emit:hold]，未完成尾部留在 pending。
	if hold > emit {
		_, _ = d.w.Write(b[emit:hold])
	}
	if hold > 0 {
		n := copy(d.pending, d.pending[hold:])
		d.pending = d.pending[:n]
	}
}

// switchToACP 把 b[:emit] 以 UTF-8 写出，其余字节改由 ACP 解码，并锁存模式。
func (d *acpByteDecoder) switchToACP(b []byte, emit int, eof bool) {
	if emit > 0 {
		_, _ = d.w.Write(b[:emit])
	}
	n := copy(d.pending, b[emit:])
	d.pending = d.pending[:n]
	d.mode = acpModeACP
	d.drainACP(eof)
}

// drainACP 把待定字节里完整的 ACP 字符解码为 UTF-8 写出；
// 跨分块的双字节字符前导字节保留到下一块（EOF 时一并解码）。
func (d *acpByteDecoder) drainACP(eof bool) {
	if len(d.pending) == 0 {
		return
	}
	text, consumed := decodeACPPrefix(d.codepage, d.pending, eof)
	if consumed == 0 {
		// 平台没有可用的 ACP 解码器：不应在此模式下出现。保险起见清空，
		// 防止待定缓冲区无界增长。
		d.pending = d.pending[:0]
		return
	}
	_, _ = io.WriteString(d.w, text)
	n := copy(d.pending, d.pending[consumed:])
	d.pending = d.pending[:n]
}

// asciiOnly 报告 p 是否全部为 7 位 ASCII（任何代码页下都同形）。
func asciiOnly(p []byte) bool {
	for _, b := range p {
		if b >= 0x80 {
			return false
		}
	}
	return true
}
