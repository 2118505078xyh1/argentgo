package mcp

import "sync"

// stderrRingBytes 限制为诊断保留的子进程 stderr 大小。
const stderrRingBytes = 8 * 1024

// ringBuffer 保留写入它的最后 n 个字节。
type ringBuffer struct {
	mu    sync.Mutex
	buf   []byte
	limit int
}

func (r *ringBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.limit <= 0 {
		r.limit = stderrRingBytes
	}
	// 单次大于上限的写入绝不能被完整追加：
	// 那会分配一个巨大的切片，却只丢弃除尾部外的全部内容。
	// 保留一个固定容量的缓冲区，只复制我们打算保留的尾部。
	if len(p) >= r.limit {
		if cap(r.buf) < r.limit {
			r.buf = make([]byte, r.limit)
		}
		r.buf = r.buf[:r.limit]
		copy(r.buf, p[len(p)-r.limit:])
		return len(p), nil
	}
	// 小写入在同一固定容量内滑动，因此无论写入多少字节，
	// 底层数组都只分配一次且永不增长。
	if cap(r.buf) < r.limit {
		buf := make([]byte, 0, r.limit)
		buf = append(buf, r.buf...)
		r.buf = buf
	}
	keep := r.limit - len(p)
	if len(r.buf) > keep {
		drop := len(r.buf) - keep
		copy(r.buf, r.buf[drop:])
		r.buf = r.buf[:keep]
	}
	r.buf = append(r.buf, p...)
	return len(p), nil
}

func (r *ringBuffer) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.buf)
}
