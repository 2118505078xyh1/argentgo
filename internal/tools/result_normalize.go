package tools

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	emptyToolResultSentinel        = "(empty tool result)"
	unrenderableToolResultSentinel = "(unrenderable tool result)"
	maxNormalizedResultBytes       = 64 * 1024
	resultEllipsis                 = "…"
)

// NormalizeToolResult turns any tool return value into bounded, display-safe
// text. It accepts string, []byte, json.RawMessage, fmt.Stringer (and error),
// and falls back to json.Marshal for everything else. ANSI escape sequences and
// control bytes (except '\n' and '\t') are removed, and output longer than
// maxNormalizedResultBytes is truncated on a UTF-8 rune boundary with an
// ellipsis. Nil or content that cleans away to nothing becomes a readable
// sentinel; a marshal failure still returns non-empty text alongside the
// wrapped error so callers always have something to show.
//
// This is a standalone seam: callers decide where in the dispatch path to use
// it, so it is deliberately not wired into any existing tool.
func NormalizeToolResult(raw any) (text string, err error) {
	var rendered string
	switch v := raw.(type) {
	case nil:
		return emptyToolResultSentinel, nil
	case string:
		rendered = v
	case json.RawMessage:
		rendered = string(v)
	case []byte:
		rendered = string(v)
	case fmt.Stringer:
		rendered = v.String()
	case error:
		rendered = v.Error()
	default:
		b, marshalErr := json.Marshal(raw)
		if marshalErr != nil {
			return unrenderableToolResultSentinel,
				fmt.Errorf("tools: normalizing tool result: %w", marshalErr)
		}
		rendered = string(b)
	}

	text = stripANSIAndControls(rendered)
	text = truncateResultBytes(text, maxNormalizedResultBytes)
	if text == "" {
		return emptyToolResultSentinel, nil
	}
	return text, nil
}

// truncateResultBytes shortens s to at most limit bytes without splitting a
// UTF-8 rune, leaving room for the ellipsis marker.
func truncateResultBytes(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	room := limit - len(resultEllipsis)
	if room < 0 {
		room = 0
	}
	cut := room
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + resultEllipsis
}

// stripANSIAndControls removes ANSI escape sequences (CSI, OSC and bare ESC
// sequences) and control bytes, keeping printable text, '\n' and '\t'.
func stripANSIAndControls(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		if c == 0x1b { // ESC
			i++
			if i >= len(s) {
				break
			}
			switch s[i] {
			case '[': // CSI: consumed through a final byte in 0x40..0x7e.
				i++
				for i < len(s) {
					last := s[i]
					i++
					if last >= 0x40 && last <= 0x7e {
						break
					}
				}
			case ']': // OSC: consumed through BEL (0x07) or ST (ESC '\\').
				i++
				for i < len(s) {
					if s[i] == 0x07 {
						i++
						break
					}
					if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
						i += 2
						break
					}
					i++
				}
			default: // Other escape sequences: drop ESC and this one byte.
				i++
			}
			continue
		}
		if c == '\n' || c == '\t' {
			b.WriteByte(c)
			i++
			continue
		}
		if c < 0x20 || c == 0x7f {
			i++ // any other ASCII control byte
			continue
		}
		if c < 0x80 {
			b.WriteByte(c)
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r >= 0x80 && r <= 0x9f {
			i += size // C1 control byte encoded in UTF-8
			continue
		}
		b.WriteString(s[i : i+size])
		i += size
	}
	return b.String()
}
