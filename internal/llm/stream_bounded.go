package llm

import (
	"sync"
	"unicode/utf8"
)

// 本文件提供一个纯内存、并发安全的有界字节累积器。它不接触任何协议、
// 连接或 I/O，也没有内置调用方，因此既不改变任何 wire 行为，也不影响
// 现有 Chatter / hook 的众多 fake 实现。它只依赖标准库。

const (
	// defaultBoundedBufferLimit 是 limit<=0 时采用的上限。取 4KiB 足以
	// 容纳一段较长的诊断输出，同时不会让日志/提示膨胀。
	defaultBoundedBufferLimit = 4096

	// boundedBufferMarker 是超限后插入头部与尾部之间的截断标记。保持纯
	// ASCII，使拼接结果仍是合法 UTF-8，便于调用方直接转 string。
	boundedBufferMarker = "...[truncated]..."
)

// BoundedBuffer 把写入的字节累积到一个有上限的缓冲区。
//
// 未超限时 Bytes() 返回全部写入内容；一旦累计字节数超过 limit，缓冲区
// 进入截断态：只保留流开头的"头部"与流末尾的"尾部"，中间用截断标记连接。
// 头尾的切分都落在 rune 边界上，因此 CJK、emoji 等多字节字符不会被切坏。
//
// 零值即可直接使用（内部按需以默认上限初始化）。所有方法由同一把互斥锁
// 保护，可在多个 goroutine 间安全共享；Bytes() 返回的是副本，调用方任意
// 修改都不会影响内部状态。
type BoundedBuffer struct {
	mu sync.Mutex

	limit      int    // 输出总字节上限
	marker     string // 截断标记（limit 极小时会被截短）
	headBudget int    // 头部保留的真实字节预算
	tailBudget int    // 尾部保留的真实字节预算

	data      []byte // 未截断: 全部内容; 已截断: 头部++尾部（不含标记）
	headLen   int    // 已截断时 data[:headLen] 为头部，其余为尾部
	truncated bool   // 进入过截断态后保持 true，直到 Reset
}

// NewBoundedBuffer 构造一个上限为 limit 字节的缓冲区。
// limit<=0 时改用 defaultBoundedBufferLimit。
func NewBoundedBuffer(limit int) *BoundedBuffer {
	if limit <= 0 {
		limit = defaultBoundedBufferLimit
	}
	b := &BoundedBuffer{}
	b.configure(limit)
	return b
}

// configure 依据 limit 计算标记与头尾预算。
//
// 尾部按 rune 边界对齐时可能比预算多出至多 utf8.UTFMax-1 字节（起始位置
// 被推进到下一个 rune 起点），因此预留同样大小的余量，确保最终
// 头部 + 标记 + 尾部的长度严格不超过 limit。
func (b *BoundedBuffer) configure(limit int) {
	b.limit = limit
	marker := boundedBufferMarker
	if len(marker) > limit {
		// 极限小的上限：连标记一并截短，保证输出不超上限。
		marker = marker[:limit]
	}
	b.marker = marker

	reserve := utf8.UTFMax - 1
	budget := 0
	if limit > len(marker)+reserve {
		budget = limit - len(marker) - reserve
	}
	head := (budget + 1) / 2
	b.headBudget, b.tailBudget = head, budget-head
}

// ensureInit 让零值 BoundedBuffer 也可用：首次使用时套用默认上限。
// 调用方必须已持有 b.mu。
func (b *BoundedBuffer) ensureInit() {
	if b.limit <= 0 {
		b.configure(defaultBoundedBufferLimit)
	}
}

// Write 追加 p。p 为空时是无操作。入参 p 不会被修改，也不会被长期持有。
//
// 未超限阶段简单地拼接原始字节（因此跨多次 Write 的半个多字节字符会在
// 后续 Write 补全后才被切分）；一旦超限即进入截断态，并按 rune 边界重算
// 头尾，此后每次写入只更新尾部，头部固定为流开头。
func (b *BoundedBuffer) Write(p []byte) {
	if len(p) == 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	b.ensureInit()

	if !b.truncated {
		if len(b.data)+len(p) <= b.limit {
			b.data = append(b.data, p...)
			return
		}
		// 首次超限：逻辑流 = data ++ p，取头部与尾部。
		head := alignedPrefix(b.data, p, b.headBudget)
		tail := alignedSuffix(b.data, p, b.tailBudget)
		b.data = make([]byte, 0, len(head)+len(tail))
		b.data = append(b.data, head...)
		b.data = append(b.data, tail...)
		b.headLen = len(head)
		b.truncated = true
		return
	}

	// 已截断：头部不变，尾部并入 p 后裁到预算内。
	tail := alignedSuffix(b.data[b.headLen:], p, b.tailBudget)
	data := make([]byte, 0, b.headLen+len(tail))
	data = append(data, b.data[:b.headLen]...)
	data = append(data, tail...)
	b.data = data
}

// Bytes 返回当前内容的副本：未截断时为全部写入字节，截断时为
// 头部 + 截断标记 + 尾部。返回长度始终 <= limit。
func (b *BoundedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.ensureInit()

	if !b.truncated {
		out := make([]byte, len(b.data))
		copy(out, b.data)
		return out
	}

	out := make([]byte, 0, len(b.data)+len(b.marker))
	out = append(out, b.data[:b.headLen]...)
	out = append(out, b.marker...)
	out = append(out, b.data[b.headLen:]...)
	return out
}

// Truncated 报告缓冲区是否曾因超限而截断（Reset 后回到 false）。
func (b *BoundedBuffer) Truncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.ensureInit()
	return b.truncated
}

// Reset 清空内容并退出截断态，保留构造时的上限与标记设置。
func (b *BoundedBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.ensureInit()
	b.data = nil
	b.headLen = 0
	b.truncated = false
}

// alignedPrefix 返回逻辑拼接 a++b 的前 n 字节中、按 rune 对齐的最长前缀
// 的独立副本。结果是 <= n 的最大 rune 边界，保证不切坏多字节字符；解码时
// 会把 a 与 b 拷贝到同一连续缓冲，故跨两次 Write 的半个 rune 也能正确识别。
func alignedPrefix(a, b []byte, n int) []byte {
	if n <= 0 {
		return []byte{}
	}
	total := len(a) + len(b)
	if n > total {
		n = total
	}
	readEnd := n + utf8.UTFMax
	if readEnd > total {
		readEnd = total
	}

	tmp := make([]byte, 0, readEnd)
	aEnd := readEnd
	if aEnd > len(a) {
		aEnd = len(a)
	}
	tmp = append(tmp, a[:aEnd]...)
	if len(tmp) < readEnd {
		tmp = append(tmp, b[:readEnd-len(tmp)]...)
	}

	end := 0
	for end < n {
		_, size := utf8.DecodeRune(tmp[end:])
		if end+size > n {
			break
		}
		end += size
	}
	return tmp[:end]
}

// alignedSuffix 返回逻辑拼接 a++b 的后 n 字节中、按 rune 对齐的最短后缀
// 的独立副本。起点被推进到 >= (total-n) 的第一个 rune 起点，因此长度最多
// 为 n+utf8.UTFMax-1；只拷贝需要的尾部区间，避免为超大的单次写入分配
// 与 p 等长的临时内存。
func alignedSuffix(a, b []byte, n int) []byte {
	if n <= 0 {
		return []byte{}
	}
	total := len(a) + len(b)
	if n >= total {
		n = total
	}
	start := total - n

	readStart := start - utf8.UTFMax
	if readStart < 0 {
		readStart = 0
	}
	tmp := make([]byte, 0, total-readStart)
	if readStart < len(a) {
		tmp = append(tmp, a[readStart:]...)
	}
	bFrom := readStart - len(a)
	if bFrom < 0 {
		bFrom = 0
	}
	tmp = append(tmp, b[bFrom:]...)

	off := start - readStart
	i := 0
	for i < off {
		_, size := utf8.DecodeRune(tmp[i:])
		if i+size > off {
			break
		}
		i += size
	}
	return tmp[i:]
}
