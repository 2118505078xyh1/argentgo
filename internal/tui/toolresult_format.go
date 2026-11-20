package tui

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

// formatToolResultContent turns a raw tool-result string into display lines.
// It is a pure formatter with no renderer dependency: structured payloads that
// MCP tools commonly emit (pretty-printed JSON, unified diffs) are kept as
// readable per-line text instead of being flattened into one unreadable blob.
// Plain text falls back to ordinary line splitting.
//
// The output is capped at maxLines rows (the last slot becomes a "… N more
// lines" marker when content is dropped) and never splits a UTF-8 rune: every
// break happens on the ASCII '\n' byte. No new dependencies are introduced.
func formatToolResultContent(raw string, maxLines int) []string {
	if maxLines <= 0 {
		return nil
	}

	var lines []string
	switch {
	case isJSONObjectOrArray(raw):
		lines = prettyJSONLines(raw)
	case looksLikeUnifiedDiff(raw):
		lines = splitResultLines(raw)
	default:
		lines = splitResultLines(raw)
	}
	return capResultLines(lines, maxLines)
}

// isJSONObjectOrArray reports whether raw is valid JSON whose top-level value
// is an object or an array. Scalars (strings, numbers, true/false, null) are
// deliberately excluded: indenting them adds no readability and a bare word
// should keep the plain-text path.
func isJSONObjectOrArray(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) == 0 || (trimmed[0] != '{' && trimmed[0] != '[') {
		return false
	}
	return json.Valid([]byte(trimmed))
}

// prettyJSONLines indents a validated JSON object/array with two-space steps
// and returns one string per output row. Indent is byte-preserving for UTF-8
// string values, so CJK and emoji inside the JSON survive untouched.
func prettyJSONLines(raw string) []string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(strings.TrimSpace(raw)), "", "  "); err != nil {
		// Defensive: json.Valid already passed, but never corrupt the payload.
		return splitResultLines(raw)
	}
	return splitResultLines(buf.String())
}

// looksLikeUnifiedDiff recognises the standard unified-diff framing. A hunk
// header ("@@ ...") is a strong signal on its own; otherwise both file
// headers ("--- " and "+++ ") must be present. Requiring the pair avoids
// mistaking a lone markdown horizontal rule ("---") for a diff.
func looksLikeUnifiedDiff(raw string) bool {
	hasMinus, hasPlus, hasHunk := false, false, false
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSuffix(line, "\r")
		switch {
		case strings.HasPrefix(line, "@@ "):
			hasHunk = true
		case strings.HasPrefix(line, "--- "):
			hasMinus = true
		case strings.HasPrefix(line, "+++ "):
			hasPlus = true
		}
		if hasHunk || (hasMinus && hasPlus) {
			return true
		}
	}
	return false
}

// splitResultLines breaks raw into logical lines on '\n', normalising CRLF and
// dropping the empty sentinel a final newline would otherwise create. Splitting
// on an ASCII byte keeps every multi-byte rune and ANSI escape sequence intact.
func splitResultLines(raw string) []string {
	if raw == "" {
		return nil
	}
	trimmed := strings.TrimSuffix(raw, "\n")
	trimmed = strings.TrimSuffix(trimmed, "\r")
	parts := strings.Split(trimmed, "\n")
	lines := make([]string, 0, len(parts))
	for _, part := range parts {
		lines = append(lines, strings.TrimSuffix(part, "\r"))
	}
	return lines
}

// capResultLines keeps at most maxLines rows. When content has to be dropped a
// "… N more lines" marker takes the last slot (matching the renderer's own
// preview convention), so the returned slice never exceeds the budget.
func capResultLines(lines []string, maxLines int) []string {
	if len(lines) <= maxLines {
		return lines
	}
	if maxLines <= 0 {
		return nil
	}
	kept := append([]string(nil), lines[:maxLines]...)
	if maxLines > 1 {
		kept[maxLines-1] = "… " + strconv.Itoa(len(lines)-maxLines+1) + " more lines"
	}
	return kept
}
