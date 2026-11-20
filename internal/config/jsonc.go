package config

import "strings"

// StripJSONC removes // and /* */ comments and trailing commas from a JSONC
// document, producing strict JSON that encoding/json can decode.
//
// It is a hand-written scanner: string contents (including sequences such as
// "http://..." or "/* not a comment */") are copied verbatim, so a comment-like
// fragment inside a quoted value can never be mistaken for syntax.
func StripJSONC(text string) string {
	// A UTF-8 BOM (common in files saved by PowerShell 5.x) is not JSON
	// whitespace; drop it at the shared entry so every caller can feed the
	// result straight to encoding/json.
	text = strings.TrimPrefix(text, "\ufeff")
	var out strings.Builder
	out.Grow(len(text))

	inString := false
	for i := 0; i < len(text); {
		char := text[i]
		if inString {
			out.WriteByte(char)
			if char == '\\' && i+1 < len(text) {
				// An escaped byte cannot close the string or open a comment.
				out.WriteByte(text[i+1])
				i += 2
				continue
			}
			if char == '"' {
				inString = false
			}
			i++
			continue
		}

		switch char {
		case '"':
			inString = true
			out.WriteByte(char)
			i++
		case '/':
			switch {
			case i+1 < len(text) && text[i+1] == '/':
				i += 2
				for i < len(text) && text[i] != '\n' {
					i++
				}
				if i < len(text) {
					out.WriteByte('\n') // keep line structure for error positions
					i++
				}
			case i+1 < len(text) && text[i+1] == '*':
				i += 2
				for i+1 < len(text) && !(text[i] == '*' && text[i+1] == '/') {
					if text[i] == '\n' {
						out.WriteByte('\n')
					}
					i++
				}
				if i+1 < len(text) {
					i += 2 // consume the closing */
				}
			default:
				out.WriteByte(char)
				i++
			}
		case ',':
			next := nextJSONCSignificant(text, i+1)
			if next < len(text) && (text[next] == '}' || text[next] == ']') {
				i++ // trailing comma: drop it
			} else {
				out.WriteByte(char)
				i++
			}
		default:
			out.WriteByte(char)
			i++
		}
	}
	return out.String()
}

// nextJSONCSignificant returns the index of the first byte at or after from
// that is not whitespace and not part of a comment. Callers use it only while
// outside a string, which is sufficient for trailing-comma detection.
func nextJSONCSignificant(text string, from int) int {
	for i := from; i < len(text); {
		switch {
		case text[i] == ' ' || text[i] == '\t' || text[i] == '\r' || text[i] == '\n':
			i++
		case i+1 < len(text) && text[i] == '/' && text[i+1] == '/':
			i += 2
			for i < len(text) && text[i] != '\n' {
				i++
			}
		case i+1 < len(text) && text[i] == '/' && text[i+1] == '*':
			i += 2
			for i+1 < len(text) && !(text[i] == '*' && text[i+1] == '/') {
				i++
			}
			if i+1 < len(text) {
				i += 2
			}
		default:
			return i
		}
	}
	return len(text)
}
