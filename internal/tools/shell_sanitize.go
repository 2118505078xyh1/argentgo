package tools

import (
	"strings"
	"unicode/utf8"
)

// sanitizeShellOutput makes raw shell output safe to hand to the model.
//
// It mirrors pi's shell-output sanitizer: ANSI escape sequences are consumed
// whole — dropping only ESC used to leave literal "[36m"-style garbage in
// colored logs such as vite's — other C0 controls are removed, interlinear
// annotation characters are removed, and every CR is stripped so progress
// displays cannot overwrite earlier lines in the transcript. All other runes,
// including CJK, are copied verbatim without splitting UTF-8 boundaries.
func sanitizeShellOutput(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		switch c {
		case 0x1b: // ESC starts an escape sequence, never a printable character.
			i = skipShellEscapeSequence(s, i+1)
			continue
		case '\t', '\n', '\r':
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
	// CR stripping is deliberately a separate pass, matching pi's per-chunk
	// behaviour, so an escape body is never mistaken for line content.
	return strings.ReplaceAll(b.String(), "\r", "")
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
