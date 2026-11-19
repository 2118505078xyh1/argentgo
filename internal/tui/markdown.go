package tui

import (
	"encoding/json"
	"strconv"
	"strings"
)

// markdownLines renders a block with the small subset of markdown the model
// actually emits: fenced code, inline code, bold and headings.
func markdownLines(textValue string, width int, first, rest string, p Palette) []Line {
	var out []Line
	inCode := false
	firstLine := true
	for _, raw := range strings.Split(textValue, "\n") {
		lineText := strings.TrimRight(raw, " \t")
		if strings.HasPrefix(strings.TrimSpace(lineText), "```") {
			inCode = !inCode
			continue
		}
		if inCode {
			for _, segment := range Wrap(lineText, maxInt(width-4, 1)) {
				code := NewLine()
				code.Push("  "+segment, p.muted())
				out = append(out, code)
			}
			firstLine = false
			continue
		}
		prefix := rest
		if firstLine {
			prefix = first
		}
		out = append(out, wrapStyled(inlineSpans(lineText, p), width, prefix, rest)...)
		firstLine = false
	}
	if len(out) == 0 {
		out = append(out, PlainLine(first))
	}
	return out
}

// inlineSpans splits one line into styled runs: `code`, **bold**, headings and
// bullet markers.
func inlineSpans(lineText string, p Palette) []Span {
	if strings.HasPrefix(lineText, "#") {
		body := strings.TrimSpace(strings.TrimLeft(lineText, "#"))
		return []Span{{Text: body, Style: Style{Bold: true}}}
	}
	if strings.HasPrefix(lineText, "- ") || strings.HasPrefix(lineText, "* ") {
		rest := parseInline(lineText[2:], p)
		out := []Span{{Text: "- ", Style: p.accent()}}
		return append(out, rest...)
	}
	return parseInline(lineText, p)
}

func parseInline(textValue string, p Palette) []Span {
	var spans []Span
	var buffer strings.Builder
	flush := func() {
		if buffer.Len() > 0 {
			spans = append(spans, Span{Text: buffer.String()})
			buffer.Reset()
		}
	}
	runes := []rune(textValue)
	for i := 0; i < len(runes); i++ {
		switch {
		case runes[i] == '`':
			end := i + 1
			for end < len(runes) && runes[end] != '`' {
				end++
			}
			if end < len(runes) && end > i+1 {
				flush()
				spans = append(spans, Span{Text: string(runes[i+1 : end]), Style: p.code()})
				i = end
				continue
			}
			buffer.WriteRune(runes[i])
		case runes[i] == '*' && i+1 < len(runes) && runes[i+1] == '*':
			end := i + 2
			for end+1 < len(runes) && !(runes[end] == '*' && runes[end+1] == '*') {
				end++
			}
			if end+1 < len(runes) && end > i+2 {
				flush()
				spans = append(spans, Span{Text: string(runes[i+2 : end]), Style: Style{Bold: true}})
				i = end + 1
				continue
			}
			buffer.WriteRune(runes[i])
		default:
			buffer.WriteRune(runes[i])
		}
	}
	flush()
	if len(spans) == 0 {
		spans = []Span{{Text: ""}}
	}
	return spans
}

type styledRune struct {
	r rune
	s Style
}

// wrapStyled wraps styled runs, breaking at a space when one is available and
// hard-breaking otherwise (which is the normal path for Chinese).
func wrapStyled(spans []Span, width int, first, rest string) []Line {
	prefixWidth := maxInt(TextWidth(first), TextWidth(rest))
	bodyWidth := maxInt(width-prefixWidth, 1)

	runes := make([]styledRune, 0, 32)
	for _, span := range spans {
		for _, r := range span.Text {
			runes = append(runes, styledRune{r: r, s: span.Style})
		}
	}

	var out []Line
	lineStart := 0
	used := 0
	lastSpace := -1
	for i := 0; i < len(runes); i++ {
		cell := RuneWidth(runes[i].r)
		if used+cell > bodyWidth && i > lineStart {
			end := i
			next := i
			if lastSpace > lineStart {
				end = lastSpace
				next = lastSpace + 1
			}
			out = append(out, buildStyledLine(runes[lineStart:end], prefixFor(len(out), first, rest)))
			lineStart = next
			used = 0
			for j := lineStart; j < i; j++ {
				used += RuneWidth(runes[j].r)
			}
			lastSpace = -1
		}
		used += cell
		if runes[i].r == ' ' {
			lastSpace = i
		}
	}
	out = append(out, buildStyledLine(runes[lineStart:], prefixFor(len(out), first, rest)))
	return out
}

func prefixFor(index int, first, rest string) string {
	if index == 0 {
		return first
	}
	return rest
}

func buildStyledLine(chunk []styledRune, prefix string) Line {
	line := NewLine()
	line.Push(prefix, Style{})
	for i := 0; i < len(chunk); {
		j := i
		var builder strings.Builder
		for j < len(chunk) && chunk[j].s == chunk[i].s {
			builder.WriteRune(chunk[j].r)
			j++
		}
		line.Push(builder.String(), chunk[i].s)
		i = j
	}
	return line
}

// wrappedBlock wraps plain text with a prefix on every row.
func wrappedBlock(body string, width int, first, rest string, style Style) []Line {
	var out []Line
	for _, paragraph := range strings.Split(body, "\n") {
		for _, segment := range Wrap(paragraph, width) {
			line := NewLine()
			line.Push(prefixFor(len(out), first, rest), style)
			line.Push(segment, style)
			out = append(out, line)
		}
	}
	if len(out) == 0 {
		out = append(out, PlainLine(first))
	}
	return out
}

// toolLines renders name(args-summary) plus a truncated output preview.
func toolLines(entry Entry, width int, expanded bool, p Palette) []Line {
	glyph := toolMark
	style := p.tool()
	switch {
	case entry.Failed:
		glyph, style = "✖ ", p.failure()
	case entry.Running:
		glyph, style = "◐ ", p.warning()
	}
	head := NewLine()
	head.Push(glyph, style)
	label := entry.Name
	if summary := summarizeArgs(entry.Args); summary != "" {
		label += "(" + summary + ")"
	} else {
		label += "()"
	}
	head.Push(Clip(label, maxInt(width-2, 1)), Style{Bold: true})
	out := []Line{head}

	limit := 3
	if expanded {
		limit = 20
	}
	if entry.Output != "" {
		bodyWidth := maxInt(width-2, 1)
		rest := strings.TrimRight(entry.Output, "\n")
		shown := 0
		for rest != "" && shown < limit {
			row := rest
			if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
				row, rest = rest[:nl], rest[nl+1:]
			} else {
				rest = ""
			}
			// Only wrap as many segments as there are rows left. The final
			// segment is clipped instead of wrapping an arbitrarily long row
			// into every segment before the limit check rejects them.
			for _, segment := range wrapLimited(row, bodyWidth, limit-shown) {
				line := NewLine()
				line.Push("  ", Style{})
				line.Push(Clip(segment, bodyWidth), p.muted())
				out = append(out, line)
				shown++
			}
		}
		if remaining := countLines(rest); remaining > 0 {
			line := NewLine()
			line.Push("  ", Style{})
			line.Push("… "+strconv.Itoa(remaining)+" more lines", p.dim())
			out = append(out, line)
		}
	}
	return out
}

// countLines counts rows without allocating the []string that strings.Split
// would build for an oversized tool output.
func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

// wrapFirst mirrors one step of Wrap: it returns the first wrapped segment and
// the unconsumed remainder, stopping as soon as the first break is found.
func wrapFirst(text string, width int) (string, string) {
	if width < 1 {
		width = 1
	}
	var cur []rune
	used, lastSpace := 0, -1
	for pos, r := range text {
		cell := RuneWidth(r)
		if used+cell > width && len(cur) > 0 {
			if lastSpace >= 1 {
				head := strings.TrimRight(string(cur[:lastSpace]), " ")
				tail := cur[lastSpace:]
				for len(tail) > 0 && tail[0] == ' ' {
					tail = tail[1:]
				}
				return head, string(tail) + text[pos:]
			}
			return string(cur), text[pos:]
		}
		cur = append(cur, r)
		used += cell
		if r == ' ' {
			lastSpace = len(cur) - 1
		}
	}
	return string(cur), ""
}

// wrapLimited wraps at most maxLines segments; the last one is clipped so a
// single huge row costs O(maxLines*width) work, not O(row length) segments.
func wrapLimited(text string, width, maxLines int) []string {
	if maxLines <= 0 {
		return nil
	}
	if width < 1 {
		width = 1
	}
	if text == "" {
		return []string{""}
	}
	var out []string
	rest := text
	for len(out) < maxLines-1 {
		head, tail := wrapFirst(rest, width)
		if head == "" && tail == "" {
			return out
		}
		out = append(out, head)
		if tail == "" {
			return out
		}
		rest = tail
	}
	out = append(out, Clip(rest, width))
	return out
}

// summarizeArgs picks the one argument worth showing: the path, command or
// query that makes a tool call recognisable at a glance.
func summarizeArgs(args string) string {
	var decoded map[string]any
	if err := json.Unmarshal([]byte(args), &decoded); err == nil {
		for _, key := range []string{"path", "command", "query", "pattern", "url", "name", "uri", "job"} {
			if value, ok := decoded[key].(string); ok && value != "" {
				return Clip(collapseSpace(value), 48)
			}
		}
	}
	flat := collapseSpace(args)
	if flat == "{}" || flat == "null" {
		return ""
	}
	return Clip(flat, 48)
}

func collapseSpace(textValue string) string {
	return strings.Join(strings.Fields(textValue), " ")
}
