package tui

import "strings"

// This file keeps raw tool output from corrupting the TUI frame. Programs such
// as cargo, npm and git animate progress bars with CSI cursor/erase sequences
// and bare carriage returns. When those bytes are copied into a transcript
// Line they reach Buffer verbatim: the frame cache sees a "different" row on
// every progress tick and the stray escapes are replayed into the terminal,
// shifting the cursor for every row painted afterwards.
//
// The helpers here are pure string transforms with no renderer dependency. The
// public entry point, normalizeToolOutput, is deliberately NOT wired into the
// default preview path in toolLines: that path stays byte-for-byte identical
// for existing layout/golden tests. It is applied only by the opt-in structured
// renderer (StructuredToolResults); see structuredOutputLines.

// stripCursorControl removes ANSI escape sequences that move the cursor, erase
// part of the screen or switch terminal modes. It mirrors Crush's
// StripCursorControl using only the standard library:
//
//   - kept: SGR colour/style (CSI final byte 'm'), OSC sequences such as
//     OSC 8 hyperlinks, character-set designators and unknown escapes;
//   - stripped: CSI cursor movement (A-H, f), erase (J, K), scroll (S, T),
//     save/restore (s, u) and mode set/reset (h, l, including DEC '?' forms),
//     plus the two-byte ESC 7 / ESC 8 save/restore-cursor sequences.
//
// A truncated sequence is never dropped: leaving it intact is safer than
// guessing where it ends. Input with no ESC byte is returned unchanged.
func stripCursorControl(s string) string {
	if !strings.ContainsRune(s, 0x1b) {
		return s
	}
	var out strings.Builder
	out.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] != 0x1b {
			out.WriteByte(s[i])
			i++
			continue
		}
		if i+1 >= len(s) { // bare ESC at EOF: no sequence to classify
			out.WriteByte(s[i])
			i++
			continue
		}
		switch s[i+1] {
		case '[': // CSI: parameter/intermediate bytes, final byte 0x40..0x7e.
			end := scanCSISequence(s, i+2)
			seq := s[i:end]
			if isCursorControlCSI(seq) {
				i = end
				continue
			}
			out.WriteString(seq)
			i = end
		case ']': // OSC: keep the whole payload (BEL- or ST-terminated).
			end := scanOSCSequence(s, i+2)
			out.WriteString(s[i:end])
			i = end
		case '7', '8': // DEC save/restore cursor.
			i += 2
		default:
			// Other two-byte escapes (charset designators, ESC =, ...).
			// A longer escape simply leaves its tail to the next iteration.
			out.WriteString(s[i : i+2])
			i += 2
		}
	}
	return out.String()
}

// scanCSISequence returns the index just past the next CSI final byte starting
// at i (the byte right after ESC '['). If the input ends first, it returns
// len(s) so a truncated sequence survives intact.
func scanCSISequence(s string, i int) int {
	for i < len(s) {
		b := s[i]
		i++
		if b >= 0x40 && b <= 0x7e {
			return i
		}
	}
	return i
}

// scanOSCSequence returns the index just past an OSC terminator (BEL 0x07 or
// ST ESC '\') starting at i (the byte right after ESC ']'). An unterminated
// OSC is consumed to the end so no partial payload leaks into the frame.
func scanOSCSequence(s string, i int) int {
	for i < len(s) {
		switch {
		case s[i] == 0x07:
			return i + 1
		case s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\':
			return i + 2
		}
		i++
	}
	return len(s)
}

// isCursorControlCSI reports whether seq is a CSI sequence whose final byte
// asks the terminal to move or erase something. 'm' (SGR) and every unknown
// final byte are deliberately excluded so colour and diagnostics survive.
func isCursorControlCSI(seq string) bool {
	if len(seq) < 3 { // need at least ESC '[' and a final byte
		return false
	}
	switch seq[len(seq)-1] {
	case 'A', 'B', 'C', 'D', // cursor up / down / forward / backward
		'E', 'F', // cursor to start/end of next/previous line
		'G',      // cursor horizontal absolute
		'H', 'f', // cursor position
		'J',      // erase in display
		'K',      // erase in line
		'S', 'T', // scroll up / down
		's', 'u', // save / restore cursor
		'h', 'l': // set / reset mode (including DEC private '?' forms)
		return true
	}
	return false
}

// simulateCarriageReturns applies the "last frame wins" rule used by Crush's
// StripCursorControl: within each line the text after the final '\r' is what a
// user would see, so earlier progress frames are discarded. Splitting on '\n'
// first keeps line boundaries (and a trailing newline) unchanged. Input with
// no '\r' is returned unchanged.
func simulateCarriageReturns(s string) string {
	if !strings.ContainsRune(s, '\r') {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if idx := strings.LastIndexByte(line, '\r'); idx >= 0 {
			lines[i] = line[idx+1:]
		}
	}
	return strings.Join(lines, "\n")
}

// normalizeOutputSpace performs the two whitespace rewrites a terminal applies
// to captured output: CRLF becomes LF and every tab becomes four spaces.
// Leading tabs are expanded in place, so the indentation depth is preserved.
// A bare '\r' is left alone for simulateCarriageReturns. Input containing
// neither '\r' nor '\t' is returned byte-for-byte unchanged.
func normalizeOutputSpace(s string) string {
	if !strings.ContainsAny(s, "\r\t") {
		return s
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if !strings.ContainsRune(s, '\t') {
		return s
	}
	return strings.ReplaceAll(s, "\t", "    ")
}

// normalizeToolOutput runs the three passes in terminal order: expand
// whitespace first so CRLF is not mistaken for a bare overwrite, then drop
// cursor control bytes, then resolve the remaining carriage returns. SGR
// colour and OSC hyperlinks survive, as do CJK and emoji runes.
func normalizeToolOutput(s string) string {
	return simulateCarriageReturns(stripCursorControl(normalizeOutputSpace(s)))
}
