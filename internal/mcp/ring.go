package mcp

import (
	"strings"
	"sync"
)

// stderrRingBytes 限制为诊断保留的子进程 stderr 大小。
const stderrRingBytes = 8 * 1024

// ringBuffer 保留写入它的最后 n 个字节。
//
// buf 是一块固定容量的环形数组：start 指向最旧字节，n 为当前有效字节数。
// 小写入只覆盖尾部并按需推进 start，不做任何整体左移；
// 单次大写入只复制尾部的 limit 字节。底层数组在首次写入后永不重新分配。
type ringBuffer struct {
	mu    sync.Mutex
	buf   []byte // 长度恒为 limit（分配后），有效区间按 (start, n) 环形解释
	start int    // 最旧字节在 buf 中的下标
	n     int    // 当前有效字节数，恒 <= limit
	limit int
}

func (r *ringBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.limit <= 0 {
		r.limit = stderrRingBytes
	}
	if cap(r.buf) < r.limit {
		r.buf = make([]byte, r.limit)
	}

	// 单次大于等于上限的写入只保留尾部 limit 字节，等价于清空后写入尾部：
	// 不会为整块 p 分配内存，也不会把尾部之外的内容带进缓冲区。
	if len(p) >= r.limit {
		r.start = 0
		r.n = r.limit
		copy(r.buf, p[len(p)-r.limit:])
		return len(p), nil
	}

	// 小写入：从 (start+n) 处顺序写 p，必要时在数组末尾环回一次。
	// 因为 len(p) < limit，单次写入最多跨两段，不会绕回起点覆盖尚未腾空的数据。
	pos := r.start + r.n
	if pos >= r.limit {
		pos -= r.limit
	}
	first := r.limit - pos
	if first > len(p) {
		first = len(p)
	}
	copy(r.buf[pos:pos+first], p[:first])
	if first < len(p) {
		copy(r.buf, p[first:])
	}

	// 覆盖超过容量时，被写掉的正是最旧的那 (n+len(p)-limit) 个字节，
	// 把 start 同步前移即可，无需搬移任何有效字节。
	newN := r.n + len(p)
	if newN <= r.limit {
		r.n = newN
	} else {
		r.start += newN - r.limit
		if r.start >= r.limit {
			r.start -= r.limit
		}
		r.n = r.limit
	}
	return len(p), nil
}

func (r *ringBuffer) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.n == 0 {
		return ""
	}
	end := r.start + r.n
	if end <= r.limit {
		// 未跨尾：一次转换即可，无中间拷贝。
		return string(r.buf[r.start:end])
	}
	// 跨尾：用 Builder 直接拼两段，避免先构造 []byte 再转 string 的额外拷贝。
	var b strings.Builder
	b.Grow(r.n)
	b.Write(r.buf[r.start:])
	b.Write(r.buf[:end-r.limit])
	return b.String()
}
