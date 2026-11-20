package proc

import (
	"io"
	"sync"
	"unicode/utf8"
)

// maxCarryBytes is the largest incomplete UTF-8 sequence that can exist: a
// rune spans at most utf8.UTFMax bytes, so before the final byte arrives there
// are at most utf8.UTFMax-1 bytes to hold back.
const maxCarryBytes = utf8.UTFMax - 1

// utf8CarryWriter turns a stream of arbitrary byte chunks into valid UTF-8 for
// its underlying writer. os/exec hands a child's stdout/stderr to an io.Writer
// in chunks whose boundaries have nothing to do with rune boundaries; a
// multi-byte rune (common with zh-CN output) can be cut in half. Holding the
// trailing incomplete sequence back and prepending it to the next chunk keeps
// the captured bytes well-formed, so rune-wise decoding never yields U+FFFD
// just because a chunk boundary fell inside a rune.
//
// It is safe for concurrent use. In the common path each stream is fed by a
// single exec copy goroutine and Flush runs only after cmd.Wait observed those
// copies finish. That invariant does not hold when awaitOutcome stops waiting
// for a child whose descendant kept a pipe open: the copy goroutine can still
// be writing while Run flushes and reads the capture. The mutex serialises
// carry state and the downstream writes in both cases.
type utf8CarryWriter struct {
	mu       sync.Mutex
	w        io.Writer
	carry    []byte
	maxCarry int // 0 means maxCarryBytes; tests shrink it to exercise the cap
}

func newUTF8CarryWriter(w io.Writer) *utf8CarryWriter {
	return &utf8CarryWriter{w: w, maxCarry: maxCarryBytes}
}

// setWriter redirects future complete runes to w. It does not flush the
// pending carry: callers that swap destinations flush first, so any dangling
// sequence is written to the old destination and no bytes are lost. The lock
// keeps the swap from racing a concurrent Write.
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

// Write implements io.Writer. Complete runes (including invalid bytes, which
// utf8 treats as width-1 runes) pass straight through; only a trailing,
// still-incomplete sequence is retained. It always reports len(p), matching
// cappedBuffer's never-fail Write contract: the underlying writers used here
// (cappedBuffer, bytes.Buffer) cannot fail, so their errors are intentionally
// ignored rather than surfaced into a process capture path that has no way to
// act on them.
func (cw *utf8CarryWriter) Write(p []byte) (int, error) {
	cw.mu.Lock()
	defer cw.mu.Unlock()
	n := len(p)
	if cw.w == nil || (len(cw.carry) == 0 && n == 0) {
		return n, nil
	}

	data := p
	if len(cw.carry) > 0 {
		// Copy into a fresh buffer: data must not alias cw.carry's backing
		// array, which is reused for the next carry below.
		data = make([]byte, 0, len(cw.carry)+n)
		data = append(data, cw.carry...)
		data = append(data, p...)
		cw.carry = cw.carry[:0]
	}

	// Split into a fully decodable prefix and an incomplete trailing sequence.
	// utf8.FullRune is false only when the leading byte is valid but the
	// continuation bytes seen so far are not enough to complete the rune.
	split := 0
	for split < len(data) {
		if !utf8.FullRune(data[split:]) {
			break
		}
		_, size := utf8.DecodeRune(data[split:])
		split += size
	}

	tail := data[split:]
	if limit := cw.limit(); len(tail) > limit {
		// Defensive cap for malformed input: flush the excess leading tail
		// bytes as-is and carry at most limit bytes. With the default limit
		// this branch is unreachable (a valid UTF-8 rune is at most
		// utf8.UTFMax bytes), but it guarantees bounded memory regardless.
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

// Flush emits any retained trailing bytes at EOF. A dangling sequence has no
// future chunk to complete it, so it is written through verbatim; rune-wise
// decoding of those bytes (range, []rune, JSON, ...) then yields U+FFFD, the
// standard Go replacement behaviour. Flush is idempotent.
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
