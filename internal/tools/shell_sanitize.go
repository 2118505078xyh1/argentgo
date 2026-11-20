package tools

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// sanitizeShellOutput makes raw shell output safe to hand to the model.
//
// Pipeline: ANSI escape sequences are consumed whole (dropping only ESC used
// to leave literal "[36m"-style garbage in colored logs such as vite's), other
// C0 controls are removed, interlinear annotation characters are removed.
// The surviving text is then normalized the way a terminal would render it:
// carriage returns overwrite line content so only the last progress frame
// remains, backspaces destructively erase the preceding character, and runs of
// identical lines are collapsed with a marker. Runes, including CJK, are never
// split across UTF-8 boundaries.
func sanitizeShellOutput(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		switch c {
		case 0x1b: // ESC starts an escape sequence, never a printable character.
			i = skipShellEscapeSequence(s, i+1)
			continue
		case '\t', '\n', '\r', '\b':
			b.WriteByte(c)
			i++
			continue
		}
		if c < 0x20 { // remaining C0 controls -> drop
			i++
			continue
		}
		if c < utf8.RuneSelf {
			b.WriteByte(c)
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if size == 0 { // defensive: non-empty input always decodes at least one byte
			size = 1
		}
		if r >= 0xfff9 && r <= 0xfffb { // interlinear annotation -> drop
			i += size
			continue
		}
		// Keep the original bytes, including an invalid UTF-8 byte, rather
		// than substituting a rune and corrupting the surrounding text.
		b.WriteString(s[i : i+size])
		i += size
	}
	// Strip escape bodies before interpreting overwrites: ANSI sequences take
	// up no columns on a real terminal, so counting them would misalign the
	// cursor and corrupt progress frames. Run the terminal-style passes only
	// on the cleaned text, and compress repeated lines last.
	text := b.String()
	text = processCarriageReturns(text)
	text = processBackspaces(text)
	return applyRunLengthEncoding(text)
}

// processCarriageReturns simulates a real terminal: within each line, a CR
// moves the cursor back to column zero and the following runes overwrite what
// is already there. That leaves only the final frame of a progress display.
//
// Cline's reference implementation works on UTF-16 code units and pads a space
// when a partial overwrite splits a surrogate pair. Overwriting here is done
// on whole runes, so a multi-byte character is replaced as a unit and can
// never be split — no padding is needed and CJK/emoji stay intact.
func processCarriageReturns(s string) string {
	if !strings.ContainsRune(s, '\r') {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if strings.ContainsRune(line, '\r') {
			lines[i] = processCarriageReturnLine(line)
		}
	}
	return strings.Join(lines, "\n")
}

func processCarriageReturnLine(line string) string {
	out := make([]rune, 0, utf8.RuneCountInString(line))
	cursor := 0
	for _, r := range line {
		if r == '\r' {
			cursor = 0
			continue
		}
		if cursor < len(out) {
			out[cursor] = r
		} else {
			out = append(out, r)
		}
		cursor++
	}
	return string(out)
}

// processBackspaces treats a backspace as destructive deletion of the rune
// immediately before it, matching the terminal-output optimization used by
// Cline/Roo-Code for progress spinners. Leading backspaces and those with
// nothing left to erase are no-ops.
func processBackspaces(s string) string {
	if !strings.ContainsRune(s, '\b') {
		return s
	}
	out := make([]rune, 0, utf8.RuneCountInString(s))
	for _, r := range s {
		if r == '\b' {
			if len(out) > 0 {
				out = out[:len(out)-1]
			}
			continue
		}
		out = append(out, r)
	}
	return string(out)
}

// applyRunLengthEncoding collapses consecutive identical lines into the first
// line plus a "<previous line repeated N additional times>" marker. The
// marker is only used when the compressed form is shorter than the original
// run; repeated short lines are left untouched.
func applyRunLengthEncoding(s string) string {
	if s == "" {
		return s
	}
	var out strings.Builder
	out.Grow(len(s))

	prevLine := ""
	hasPrev := false
	repeat := 0

	flush := func() {
		if !hasPrev {
			return
		}
		if repeat == 0 {
			out.WriteString(prevLine)
		} else {
			desc := fmt.Sprintf("<previous line repeated %d additional times>\n", repeat)
			lineLen := utf8.RuneCountInString(prevLine)
			if len(desc) < lineLen*repeat {
				out.WriteString(prevLine)
				out.WriteString(desc)
			} else {
				for i := 0; i <= repeat; i++ {
					out.WriteString(prevLine)
				}
			}
		}
		repeat = 0
	}

	for s != "" {
		idx := strings.IndexByte(s, '\n')
		var line string
		if idx < 0 {
			line = s
			s = ""
		} else {
			line = s[:idx+1]
			s = s[idx+1:]
		}
		if !hasPrev {
			prevLine = line
			hasPrev = true
		} else if line == prevLine {
			repeat++
		} else {
			flush()
			prevLine = line
		}
	}
	flush()
	return out.String()
}

// skipShellEscapeSequence consumes one escape sequence body starting just
// after the ESC byte and returns the index of the first following byte. It
// handles CSI (ESC '[' ... final byte 0x40..0x7e), OSC (ESC ']' ... BEL or
// ESC '\'), charset designators (ESC '(' X / ESC ')' X), and a generic escape
// that swallows one following character. A sequence truncated at end of input
// is consumed to the end instead of panicking or leaking its body.
func skipShellEscapeSequence(s string, i int) int {
	if i >= len(s) {
		return i
	}
	switch s[i] {
	case '[': // CSI: parameter/intermediate bytes, final byte 0x40..0x7e.
		i++
		for i < len(s) {
			last := s[i]
			i++
			if last >= 0x40 && last <= 0x7e {
				break
			}
		}
	case ']': // OSC: terminated by BEL (0x07) or ST (ESC '\').
		i++
		for i < len(s) {
			c := s[i]
			if c == 0x07 {
				i++
				break
			}
			if c == 0x1b {
				i++
				if i < len(s) && s[i] == '\\' {
					i++
				}
				break
			}
			i++
		}
	case '(', ')': // charset designator: exactly one following byte.
		i++
		if i < len(s) {
			i++
		}
	default: // any other escape swallows one following byte.
		i++
	}
	return i
}
