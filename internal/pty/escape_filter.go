package pty

// stripTerminalQueries removes terminal query/report escape sequences from one
// chunk of PTY output and returns the clean bytes plus any trailing partial
// escape sequence that must be prepended to the next chunk before it is passed
// here again.
//
// Why this exists: CLIs such as gh/npm — and, worse, attacker-controlled tool
// output — emit sequences that ask the terminal to report its own state:
//
//   - DSR:              ESC [ 6 n  (and ESC [ 5 n)
//   - OSC colour query: ESC ] 11 ; ? BEL  (or ESC ] 10 ; ? ST, ESC ] 4 ; n ; ? …)
//   - DA / DA2:         ESC [ c    (and ESC [ > c)
//   - DECRQSS:          ESC [ $ q
//
// A real terminal answers each of these by injecting *more* escape sequences
// into the byte stream. In argentgo that stream is rendered by the TUI/native
// frontends and echoed into the model, so the answer becomes a prompt-injection
// channel. Legitimate display sequences (SGR colour/bold, cursor movement,
// charset designators, OSC hyperlinks/titles) are kept byte-for-byte.
//
// The filter is byte-oriented and only ever holds back ASCII escape bytes, so a
// UTF-8 rune split across chunk boundaries is emitted intact; keeping the
// stream well-formed is the caller's UTF-8 carry writer's job, not this one's.
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
			// Copy rather than alias: callers reuse their Read buffer, so the
			// pending bytes must not point into chunk's backing array.
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

// scanEscape parses the escape sequence beginning at chunk[i] (which must be
// ESC). It returns the index just past the sequence, whether it is a terminal
// query that should be dropped, and whether the sequence is incomplete in this
// chunk and must be held back.
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

// scanCSI parses a CSI whose body starts at body (just after ESC '[').
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
		// Consume up to (not including) this byte and let the caller
		// resynchronise on it, rather than swallowing unrelated text.
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

// scanOSC parses an OSC whose body starts at body (just after ESC ']'). An OSC
// is a query when its payload contains the ";?" pattern (OSC 10/11/12/4/52
// queries). ST is ESC '\'; BEL also terminates OSC.
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

// scanStringEscape parses a DCS/SOS/PM/APC body (after its introducer) up to a
// string terminator. These are never queries, but they must still be tracked so
// a terminator split across chunks is not torn apart.
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

// queryFilter is a streaming wrapper around stripTerminalQueries: it keeps the
// trailing partial sequence between Write calls, so callers can feed it chunks
// from repeated Read calls without managing pending bytes themselves. It is not
// safe for concurrent use — each PTY stream has a single reader goroutine.
type queryFilter struct {
	pending []byte
}

// Write filters one chunk and returns only bytes that are safe to emit now.
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

// Flush emits any retained partial sequence at EOF. A dangling fragment cannot
// be a complete query, so it is written through verbatim. Flush is idempotent.
func (f *queryFilter) Flush() []byte {
	out := f.pending
	f.pending = nil
	return out
}
