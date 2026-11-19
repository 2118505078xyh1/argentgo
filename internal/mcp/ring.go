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
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.limit {
		r.buf = r.buf[len(r.buf)-r.limit:]
	}
	return len(p), nil
}

func (r *ringBuffer) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.buf)
}
