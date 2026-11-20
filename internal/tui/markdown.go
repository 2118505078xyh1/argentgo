package tui

import (
	"encoding/json"
	"strconv"
	"strings"
)

// markdownLines renders a block with the small subset of markdown the model
// actually emits: fenced code, inline code, bold, headings and tables.
func markdownLines(textValue string, width int, first, rest string, p Palette) []Line {
	var out []Line
	inCode := false
	firstLine := true
	// Every emitted row carries a first/rest prefix (assistant mark "◆ " or
	// its indent), so the content budget is width minus that prefix — exactly
	// the rule wrapStyled uses for ordinary lines. Tables must use the same
	// budget or their right border runs past the transcript edge.
	prefixWidth := maxInt(TextWidth(first), TextWidth(rest))
	bodyWidth := maxInt(width-prefixWidth, 1)
	lines := strings.Split(textValue, "\n")
	for i := 0; i < len(lines); {
		raw := lines[i]
		lineText := strings.TrimRight(raw, " \t")
		if strings.HasPrefix(strings.TrimSpace(lineText), "```") {
			inCode = !inCode
			i++
			continue
		}
		if inCode {
			for _, segment := range Wrap(lineText, maxInt(width-4, 1)) {
				code := NewLine()
				code.Push("  "+segment, p.muted())
				out = append(out, code)
			}
			firstLine = false
			i++
			continue
		}
		// A table block is a line with '|' immediately followed by a delimiter
		// row. Render it as a whole so columns stay aligned, then skip past it.
		if i+1 < len(lines) && strings.Contains(lineText, "|") {
			if _, ok := parseTableDelimiter(lines[i+1]); ok {
				end := i + 2
				for end < len(lines) && strings.Contains(lines[end], "|") {
					end++
				}
				for _, row := range renderMarkdownTable(lines[i:end], bodyWidth, TextWidth, Wrap) {
					prefix := rest
					if firstLine {
						prefix = first
					}
					line := NewLine()
					line.Push(prefix, Style{})
					line.Push(row, Style{})
					out = append(out, line)
					firstLine = false
				}
				i = end
				continue
			}
		}
		prefix := rest
		if firstLine {
			prefix = first
		}
		out = append(out, wrapStyled(inlineSpans(lineText, p), width, prefix, rest)...)
		firstLine = false
		i++
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

// styledCluster is one grapheme cluster with the style of the span it came
// from. Wrapping works on clusters, not runes, so a ZWJ emoji, a flag or a
// combining sequence is never split across lines (see Wrap in screen.go).
type styledCluster struct {
	text  string
	s     Style
	cells int
}

// splitGraphemes returns the grapheme clusters of s in order. nextGrapheme is
// the single authority for cluster boundaries and cell widths; reusing it here
// keeps the transcript wrapping consistent with TextWidth/Clip/Wrap.
func splitGraphemes(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for i := 0; i < len(s); {
		next, _ := nextGrapheme(s, i)
		out = append(out, s[i:next])
		i = next
	}
	return out
}

// wrapStyled wraps styled runs, breaking at a space when one is available and
// hard-breaking otherwise (which is the normal path for Chinese). Breaks are
// made between grapheme clusters so multi-rune emoji stay intact.
func wrapStyled(spans []Span, width int, first, rest string) []Line {
	prefixWidth := maxInt(TextWidth(first), TextWidth(rest))
	bodyWidth := maxInt(width-prefixWidth, 1)

	clusters := make([]styledCluster, 0, 32)
	for _, span := range spans {
		for _, text := range splitGraphemes(span.Text) {
			clusters = append(clusters, styledCluster{text: text, s: span.Style, cells: TextWidth(text)})
		}
	}

	var out []Line
	lineStart := 0
	used := 0
	lastSpace := -1
	for i := 0; i < len(clusters); i++ {
		cell := clusters[i].cells
		if used+cell > bodyWidth && i > lineStart {
			end := i
			next := i
			if lastSpace > lineStart {
				end = lastSpace
				next = lastSpace + 1
			}
			out = append(out, buildStyledLine(clusters[lineStart:end], prefixFor(len(out), first, rest)))
			lineStart = next
			used = 0
			for j := lineStart; j < i; j++ {
				used += clusters[j].cells
			}
			lastSpace = -1
		}
		used += cell
		if clusters[i].text == " " {
			lastSpace = i
		}
	}
	out = append(out, buildStyledLine(clusters[lineStart:], prefixFor(len(out), first, rest)))
	return out
}

func prefixFor(index int, first, rest string) string {
	if index == 0 {
		return first
	}
	return rest
}

func buildStyledLine(chunk []styledCluster, prefix string) Line {
	line := NewLine()
	line.Push(prefix, Style{})
	for i := 0; i < len(chunk); {
		j := i
		var builder strings.Builder
		for j < len(chunk) && chunk[j].s == chunk[i].s {
			builder.WriteString(chunk[j].text)
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
	if oldText, newText, ok := editTexts(entry.Name, entry.Args); ok {
		return append(out, diffPreviewLines(DiffLines(oldText, newText), limit, width, p)...)
	}
	if entry.Output != "" {
		if StructuredToolResults {
			out = append(out, structuredOutputLines(entry.Output, limit, width, p)...)
			return out
		}
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

// editCall covers both the edit schema and the multi_edit batch schema. Edits
// recursively holds the per-file calls of a batch.
type editCall struct {
	Old     string     `json:"old"`
	New     string     `json:"new"`
	OldText string     `json:"old_text"`
	NewText string     `json:"new_text"`
	Edits   []editCall `json:"edits"`
}

// editTexts returns the before/after text of an edit or multi_edit call. A
// non-edit tool, malformed JSON or a missing side returns ok=false so the
// caller keeps the original output-preview rendering.
func editTexts(name, args string) (oldText, newText string, ok bool) {
	if name != "edit" && name != "multi_edit" {
		return "", "", false
	}
	var call editCall
	if err := json.Unmarshal([]byte(args), &call); err != nil {
		return "", "", false
	}
	sides := func(e editCall) (string, string) {
		old, new := e.Old, e.New
		if old == "" {
			old = e.OldText
		}
		if new == "" {
			new = e.NewText
		}
		return old, new
	}
	if name == "multi_edit" {
		for _, e := range call.Edits {
			old, new := sides(e)
			if old == "" || new == "" {
				continue
			}
			if oldText != "" {
				oldText += "\n"
			}
			if newText != "" {
				newText += "\n"
			}
			oldText, newText = oldText+old, newText+new
		}
		return oldText, newText, oldText != "" && newText != ""
	}
	oldText, newText = sides(call)
	return oldText, newText, oldText != "" && newText != ""
}

// diffPreviewLines renders a diff with the same row budget and "N more lines"
// marker as the ordinary tool-output preview. Adjacent "-"/"+" rows are
// aligned word by word so a small edit highlights only the changed words.
func diffPreviewLines(diffs []DiffLine, limit, width int, p Palette) []Line {
	var out []Line
	bodyWidth := maxInt(width-4, 1)
	shown := 0
	for shown < limit && shown < len(diffs) {
		diff := diffs[shown]
		// Pairing only when both rows still fit the budget keeps the row count
		// and "N more lines" result identical to the whole-line baseline.
		if diff.Kind == DiffDel && shown+1 < limit && shown+1 < len(diffs) &&
			diffs[shown+1].Kind == DiffAdd {
			oldSegs, newSegs := WordDiff(diff.Text, diffs[shown+1].Text)
			out = append(out,
				wordDiffRow("- ", oldSegs, bodyWidth, p, false),
				wordDiffRow("+ ", newSegs, bodyWidth, p, true),
			)
			shown += 2
			continue
		}
		out = append(out, plainDiffRow(diff, bodyWidth, p))
		shown++
	}
	if remaining := len(diffs) - shown; remaining > 0 {
		line := NewLine()
		line.Push("  ", Style{})
		line.Push("… "+strconv.Itoa(remaining)+" more lines", p.dim())
		out = append(out, line)
	}
	return out
}

// plainDiffRow renders an unpaired diff row with one whole-line span, which is
// the original behaviour an isolated deletion or addition still falls back to.
func plainDiffRow(diff DiffLine, bodyWidth int, p Palette) Line {
	line := NewLine()
	line.Push("  ", Style{})
	switch diff.Kind {
	case DiffAdd:
		line.Push("+ ", p.tool())
		line.Push(Clip(diff.Text, bodyWidth), p.tool())
	case DiffDel:
		line.Push("- ", p.failure())
		line.Push(Clip(diff.Text, bodyWidth), p.failure())
	default:
		line.Push("  ", p.muted())
		line.Push(Clip(diff.Text, bodyWidth), p.muted())
	}
	return line
}

// wordDiffRow renders one side of a paired del/add row. Unchanged segments
// keep the row's ordinary style; changed segments get a strong background so
// the edited word stands out. Segments share one width budget accumulated in
// display cells, and a segment that crosses the edge is clipped on a grapheme
// boundary rather than cut mid-rune (so CJK and emoji stay intact).
func wordDiffRow(marker string, segs []WordSegment, bodyWidth int, p Palette, add bool) Line {
	line := NewLine()
	line.Push("  ", Style{})
	base, changed := p.failure(), changedWordStyle(p, false)
	if add {
		base, changed = p.tool(), changedWordStyle(p, true)
	}
	line.Push(marker, base)
	used := 0
	for _, seg := range segs {
		if used >= bodyWidth {
			break
		}
		text, style := seg.Text, base
		if seg.Changed {
			style = changed
		}
		cells := TextWidth(text)
		if room := bodyWidth - used; cells > room {
			line.Push(Clip(text, room), style)
			break
		}
		line.Push(text, style)
		used += cells
	}
	return line
}

// changedWordStyle gives a changed word a coloured background band. It reuses
// the failure/tool palette colours so themes and NO_COLOR degrade the same way
// as the rest of the renderer.
func changedWordStyle(p Palette, add bool) Style {
	if p.noColor {
		return Style{}
	}
	if add {
		return Style{Bg: p.Tool, Bold: true}
	}
	return Style{Bg: p.Error, Bold: true}
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
// the unconsumed remainder, stopping as soon as the first break is found. It
// walks grapheme clusters through nextGrapheme — the same authority Wrap, Clip
// and Buffer.WriteString use — so a ZWJ emoji, a flag or a combining sequence is
// never torn across two preview rows. Measuring each rune with RuneWidth here
// used to leave a dangling joiner or half a flag at the end of a row.
func wrapFirst(text string, width int) (string, string) {
	if width < 1 {
		width = 1
	}
	var cur []string         // grapheme clusters, in order
	used, lastSpace := 0, -1 // lastSpace indexes cur
	for pos := 0; pos < len(text); {
		next, cell := nextGrapheme(text, pos)
		cluster := text[pos:next]
		if used+cell > width && len(cur) > 0 {
			if lastSpace >= 1 {
				head := strings.TrimRight(strings.Join(cur[:lastSpace], ""), " ")
				tail := strings.Join(cur[lastSpace:], "")
				for len(tail) > 0 && tail[0] == ' ' {
					tail = tail[1:]
				}
				return head, tail + text[pos:]
			}
			return strings.Join(cur, ""), text[pos:]
		}
		cur = append(cur, cluster)
		used += cell
		if cluster == " " {
			lastSpace = len(cur) - 1
		}
		pos = next
	}
	return strings.Join(cur, ""), ""
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
