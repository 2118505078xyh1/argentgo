package tui

import (
	"encoding/json"
	"strconv"
	"strings"
)

// markdownLines renders a block with the small subset of markdown the model
// actually emits: fenced code, inline code, bold and headings.
func markdownLines(textValue string, width int, first, rest string) []Line {
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
				code.Push("  "+segment, Style{Fg: ColorGray})
				out = append(out, code)
			}
			firstLine = false
			continue
		}
		prefix := rest
		if firstLine {
			prefix = first
		}
		out = append(out, wrapStyled(inlineSpans(lineText), width, prefix, rest)...)
		firstLine = false
	}
	if len(out) == 0 {
		out = append(out, PlainLine(first))
	}
	return out
}

// inlineSpans splits one line into styled runs: `code`, **bold**, headings and
// bullet markers.
func inlineSpans(lineText string) []Span {
	if strings.HasPrefix(lineText, "#") {
		body := strings.TrimSpace(strings.TrimLeft(lineText, "#"))
		return []Span{{Text: body, Style: Style{Bold: true}}}
	}
	if strings.HasPrefix(lineText, "- ") || strings.HasPrefix(lineText, "* ") {
		rest := parseInline(lineText[2:])
		out := []Span{{Text: "- ", Style: Style{Fg: ColorCyan}}}
		return append(out, rest...)
	}
	return parseInline(lineText)
}

func parseInline(textValue string) []Span {
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
				spans = append(spans, Span{Text: string(runes[i+1 : end]), Style: Style{Fg: ColorMagenta}})
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
func toolLines(entry Entry, width int, expanded bool) []Line {
	glyph := toolMark
	style := Style{Fg: ColorGreen}
	switch {
	case entry.Failed:
		glyph, style = "✖ ", Style{Fg: ColorRed}
	case entry.Running:
		glyph, style = "◐ ", Style{Fg: ColorYellow}
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
		rows := strings.Split(strings.TrimRight(entry.Output, "\n"), "\n")
		shown := 0
		for _, row := range rows {
			for _, segment := range Wrap(row, maxInt(width-2, 1)) {
				if shown >= limit {
					break
				}
				line := NewLine()
				line.Push("  ", Style{})
				line.Push(Clip(segment, maxInt(width-2, 1)), Style{Fg: ColorGray})
				out = append(out, line)
				shown++
			}
			if shown >= limit {
				break
			}
		}
		if len(rows) > shown {
			line := NewLine()
			line.Push("  ", Style{})
			line.Push("… "+strconv.Itoa(len(rows)-shown)+" more lines", Style{Fg: ColorGray, Dim: true})
			out = append(out, line)
		}
	}
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
