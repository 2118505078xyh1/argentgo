package security

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// The Unicode Tag Block (Plane 14) mirrors printable ASCII at U+E0000..U+E007F.
// Its code points render as nothing in terminals and editors, so they can hide
// a prompt-injection payload inside text a human believes they have read.
const (
	tagBlockStart = '\U000E0000'
	tagBlockEnd   = '\U000E007F'
)

// ContainsInvisibleTags reports whether s contains any Unicode Tag Block
// character. It is a detection helper: callers decide whether to warn, reject,
// or strip. Bidi controls and other invisible format characters are not tags.
func ContainsInvisibleTags(s string) bool {
	for _, r := range s {
		if r >= tagBlockStart && r <= tagBlockEnd {
			return true
		}
	}
	return false
}

// isBidiOrFormat reports whether r changes text direction or layout without
// rendering as visible content. These are the characters that must be shown as
// escapes in an approval prompt so the operator sees everything they approve.
func isBidiOrFormat(r rune) bool {
	switch r {
	case '\u061C', // ARABIC LETTER MARK
		'\u200E', // LEFT-TO-RIGHT MARK
		'\u200F', // RIGHT-TO-LEFT MARK
		'\uFEFF': // ZERO WIDTH NO-BREAK SPACE / BOM
		return true
	}
	return (r >= '\u202A' && r <= '\u202E') || // embedding and override controls
		(r >= '\u2066' && r <= '\u2069') // isolate controls
}

// EscapeBidi replaces each bidi/format character with a visible \uXXXX escape,
// leaving every other rune untouched. Non-special bytes (including malformed
// UTF-8) are copied verbatim, so the returned string is never a lossy rewrite
// of the input. CJK, emoji, newline and tab all pass through unchanged.
func EscapeBidi(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if isBidiOrFormat(r) {
			b.WriteString(fmt.Sprintf("\\u%04x", r))
		} else {
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String()
}

// SanitizeInvisible removes Unicode Tag Block characters entirely and escapes
// bidi/format characters. hadTags reports whether any tag was found, so the
// caller can warn the operator that hidden text was discarded. Only the prompt
// copy is sanitized; the executed arguments are never modified.
func SanitizeInvisible(s string) (cleaned string, hadTags bool) {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r >= tagBlockStart && r <= tagBlockEnd {
			hadTags = true
			i += size
			continue
		}
		if isBidiOrFormat(r) {
			b.WriteString(fmt.Sprintf("\\u%04x", r))
		} else {
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String(), hadTags
}
