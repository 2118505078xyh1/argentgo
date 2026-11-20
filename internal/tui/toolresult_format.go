package tui

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

// StructuredToolResults opts tool-result previews into the structured renderer:
// JSON objects/arrays are pretty-printed, unified diffs keep their framing, and
// plain text falls back to ordinary line splitting (see formatToolResultContent).
//
// It is OFF by default. The original preview rendering in toolLines is kept as
// the canonical, byte-for-byte path so existing layout and golden tests do not
// change; flip this switch to preview the structured path. Keeping the switch
// as a plain package variable lets a later change wire it to a CLI flag/config
// without touching the renderer.
var StructuredToolResults = false

// structuredOutputLines renders a tool result through formatToolResultContent.
// The formatter already caps the row count and appends its own "… N more lines"
// marker, so this helper only applies the visual prefix and styles and never
// adds a second overflow marker. The marker row uses the dimmer style to match
// the original preview's overflow line.
func structuredOutputLines(raw string, limit, width int, p Palette) []Line {
	bodyWidth := maxInt(width-2, 1)
	// This renderer is only reached when StructuredToolResults is enabled. The
	// legacy byte-for-byte preview in toolLines stays untouched; sanitising
	// here keeps progress-bar cursor escapes out of the frame cache and the
	// rows painted after a tool result.
	textLines := formatToolResultContent(normalizeToolOutput(raw), limit)
	out := make([]Line, 0, len(textLines))
	for _, text := range textLines {
		line := NewLine()
		line.Push("  ", Style{})
		if strings.HasPrefix(text, "… ") && strings.HasSuffix(text, " more lines") {
			line.Push(text, p.dim())
		} else {
			line.Push(Clip(text, bodyWidth), p.muted())
		}
		out = append(out, line)
	}
	return out
}

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
