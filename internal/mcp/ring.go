package mcp

import "sync"

// stderrRingBytes bounds the child stderr kept for diagnostics.
const stderrRingBytes = 8 * 1024

// ringBuffer keeps the last n bytes written to it.
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
	// A single write larger than the limit must never be appended in full:
	// that would allocate a huge slice only to discard all but its tail.
	// Keep one fixed-capacity buffer and copy only the tail we intend to keep.
	if len(p) >= r.limit {
		if cap(r.buf) < r.limit {
			r.buf = make([]byte, r.limit)
		}
		r.buf = r.buf[:r.limit]
		copy(r.buf, p[len(p)-r.limit:])
		return len(p), nil
	}
	// Small writes slide within the same fixed capacity, so the backing array
	// is allocated once and never grows no matter how many bytes are written.
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
