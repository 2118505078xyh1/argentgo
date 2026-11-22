package proc

import (
	"io"
	"sync"
	"unicode/utf8"
)

// maxCarryBytes 是可能存在的最大不完整 UTF-8 序列：一个
// 字符最多占 utf8.UTFMax 字节，因此在最后一个字节到达前，
// 最多有 utf8.UTFMax-1 字节需要暂存。
const maxCarryBytes = utf8.UTFMax - 1

// utf8CarryWriter 把任意字节块组成的流转换为底层 writer 可接受的合法 UTF-8。
// os/exec 把子进程的 stdout/stderr 分块交给 io.Writer，而这些分块的边界
// 与字符边界无关；一个多字节字符（中文输出中很常见）可能被切开。
// 把尾部不完整的序列暂存并拼到下一个分块之前，
// 可保持捕获字节格式正确，使按字符解码
// 绝不会仅因分块边界落在字符内部就产生 U+FFFD。
// 仅因分块边界落在字符内部。
//
// 它可安全并发使用。常见路径下每个流由
// 单个 exec 拷贝 goroutine 喂入，Flush 只在 cmd.Wait 观察到那些
// 拷贝完成后才运行。当 awaitOutcome 停止等待某个后代仍持有管道的
// 子进程时，该不变量不再成立：Run 刷新并读取捕获内容时，
// 拷贝 goroutine 可能仍在写入。两种情况下互斥锁都会串行化
// 暂存状态和下游写入。
type utf8CarryWriter struct {
	mu       sync.Mutex
	w        io.Writer
	carry    []byte
	scratch  []byte // 复用的拼接缓冲区，避免携带续字节写入时每次 make
	maxCarry int    // 0 表示 maxCarryBytes；测试会缩小它以演练上限
}

func newUTF8CarryWriter(w io.Writer) *utf8CarryWriter {
	return &utf8CarryWriter{w: w, maxCarry: maxCarryBytes}
}

// setWriter 把之后完整的字符重定向到 w。它不会刷新
// 待定的暂存内容：更换目标的调用方会先刷新，因此任何悬空
// 序列都会写入旧目标，不会丢失字节。锁
// 使替换不会与并发的 Write 竞争。
func (cw *utf8CarryWriter) setWriter(w io.Writer) {
	if cw == nil {
		return
	}
	cw.mu.Lock()
	defer cw.mu.Unlock()
	cw.w = w
}

func (cw *utf8CarryWriter) limit() int {
	if cw.maxCarry > 0 {
		return cw.maxCarry
	}
	return maxCarryBytes
}

// Write 实现 io.Writer。完整的字符（包括无效字节，
// utf8 将其视为宽度为 1 的字符）直接通过；只有尾部的、
// 仍不完整的序列会被保留。它总是报告 len(p)，
// 符合 cappedBuffer 的 Write 永不失败约定：这里使用的底层 writer
// （cappedBuffer、bytes.Buffer）不会失败，因此它们的错误被
// 有意忽略，而不是冒到进程捕获路径中——那条路径对此
// 无能为力。
func (cw *utf8CarryWriter) Write(p []byte) (int, error) {
	cw.mu.Lock()
	defer cw.mu.Unlock()
	n := len(p)
	if cw.w == nil || (len(cw.carry) == 0 && n == 0) {
		return n, nil
	}

	data := p
	if len(cw.carry) > 0 {
		// 复用 scratch 拼接暂存尾部与新分块：仅在容量不足时才增长，
		// 消除原先每次携带写入都 make 一个新切片的分配。
		// 副本不得与 cw.carry 共享底层数组，该数组会被下面的暂存逻辑再次复用。
		cw.scratch = append(cw.scratch[:0], cw.carry...)
		cw.scratch = append(cw.scratch, p...)
		data = cw.scratch
		cw.carry = cw.carry[:0]
	}

	// 拆分为完全可解码的前缀和不完整的尾部序列。
	// 每个位置只解码一次：合法字符（含 ASCII 与 U+FFFD 自身）的 size 足以推进；
	// 只有 (RuneError, 1) 这一结果同时覆盖"非法字节"和"末尾不完整序列"，
	// 此时才用 FullRune 做边界判断——不完整序列停下并暂存，
	// 非法字节按宽度 1 原样通过。相较原先每 rune 先 FullRune 再 DecodeRune，
	// 常见路径少一次扫描，且语义与优化前逐字节一致。
	split := 0
	for split < len(data) {
		r, size := utf8.DecodeRune(data[split:])
		if r == utf8.RuneError && size == 1 {
			if !utf8.FullRune(data[split:]) {
				break
			}
			split++ // 非法字节：按宽度 1 通过
			continue
		}
		split += size
	}

	tail := data[split:]
	if limit := cw.limit(); len(tail) > limit {
		// 针对畸形输入的防御性上限：把超出的前导尾部
		// 字节原样刷新，最多暂存 limit 字节。在默认上限下
		// 该分支不可达（一个合法的 UTF-8 字符最多占
		// utf8.UTFMax 字节），但它保证内存始终有界。
		excess := len(tail) - limit
		_, _ = cw.w.Write(data[:split+excess])
		cw.carry = append(cw.carry[:0], tail[excess:]...)
	} else {
		if split > 0 {
			_, _ = cw.w.Write(data[:split])
		}
		cw.carry = append(cw.carry[:0], tail...)
	}
	return n, nil
}

// Flush 在 EOF 时发出任何保留的尾部字节。悬空序列不会有
// 后续分块来补全，因此被原样写出；随后按字符解码这些字节
// （range、[]rune、JSON 等）会得到 U+FFFD，这是 Go 标准的
// 替换行为。Flush 是幂等的。
func (cw *utf8CarryWriter) Flush() {
	if cw == nil {
		return
	}
	cw.mu.Lock()
	defer cw.mu.Unlock()
	if cw.w == nil || len(cw.carry) == 0 {
		return
	}
	_, _ = cw.w.Write(cw.carry)
	cw.carry = cw.carry[:0]
}
