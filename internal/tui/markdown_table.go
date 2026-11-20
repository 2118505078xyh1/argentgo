package tui

import "strings"

// This file is deliberately self-contained: it adds markdown table rendering
// without touching markdown.go. The main entry point takes textWidth and wrap
// as injected functions so the layout can be tested without a Palette; passing
// nil selects the real TextWidth/Wrap implementations from this package.

type tableAlign uint8

const (
	tableAlignNone   tableAlign = iota // no colon in the delimiter cell
	tableAlignLeft                     // :---
	tableAlignCenter                   // :---:
	tableAlignRight                    // ---:
)

// renderMarkdownTable replaces every recognised markdown table block in lines
// with aligned rows. Everything else is copied through unchanged. A block is
// recognised when a line contains '|' and the immediately following line is a
// delimiter row made only of '-' and optional leading/trailing ':' per cell,
// e.g. |---|:---:|---:|. Subsequent lines containing '|' are body rows; the
// first line without '|' ends the block.
func renderMarkdownTable(lines []string, width int, textWidth func(string) int, wrap func(string, int) []string) []string {
	measure := textWidth
	if measure == nil {
		measure = TextWidth
	}
	breaker := wrap
	if breaker == nil {
		breaker = Wrap
	}

	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); {
		if i+1 < len(lines) && strings.Contains(lines[i], "|") {
			if aligns, ok := parseTableDelimiter(lines[i+1]); ok {
				header := splitTableRow(lines[i])
				if len(header) > 0 {
					end := i + 2
					var body [][]string
					for end < len(lines) && strings.Contains(lines[end], "|") {
						body = append(body, splitTableRow(lines[end]))
						end++
					}
					out = append(out, renderTableBlock(header, aligns, body, width, measure, breaker)...)
					i = end
					continue
				}
			}
		}
		out = append(out, lines[i])
		i++
	}
	return out
}

// splitTableRow splits on '|' and drops the empty ends produced by a leading
// or trailing pipe, then trims each cell. Interior empty cells are kept.
func splitTableRow(line string) []string {
	parts := strings.Split(strings.TrimSpace(line), "|")
	if len(parts) > 0 && strings.TrimSpace(parts[0]) == "" {
		parts = parts[1:]
	}
	if len(parts) > 0 && strings.TrimSpace(parts[len(parts)-1]) == "" {
		parts = parts[:len(parts)-1]
	}
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// parseTableDelimiter accepts only cells of at least three dashes with at most
// one leading and one trailing colon. The strictness is what keeps a normal
// two-column row from being mistaken for a separator.
func parseTableDelimiter(line string) ([]tableAlign, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.Contains(trimmed, "|") {
		return nil, false
	}
	cells := splitTableRow(trimmed)
	if len(cells) == 0 {
		return nil, false
	}
	aligns := make([]tableAlign, 0, len(cells))
	for _, cell := range cells {
		align, ok := parseTableDelimiterCell(cell)
		if !ok {
			return nil, false
		}
		aligns = append(aligns, align)
	}
	return aligns, true
}

func parseTableDelimiterCell(cell string) (tableAlign, bool) {
	if strings.Count(cell, ":") > 2 {
		return tableAlignNone, false
	}
	left, right := strings.HasPrefix(cell, ":"), strings.HasSuffix(cell, ":")
	core := strings.Trim(cell, ":")
	if len(core) < 3 || strings.Trim(core, "-") != "" {
		return tableAlignNone, false
	}
	switch {
	case left && right:
		return tableAlignCenter, true
	case left:
		return tableAlignLeft, true
	case right:
		return tableAlignRight, true
	default:
		return tableAlignNone, true
	}
}

// tableColumnWidths measures every cell, then shrinks the widest column one
// cell at a time until the row fits width. A row occupies 3*n+1 cells of pipe
// and space overhead: "| c0 | c1 | ... |".
func tableColumnWidths(rows [][]string, width int, measure func(string) int) []int {
	n := 0
	for _, row := range rows {
		if len(row) > n {
			n = len(row)
		}
	}
	if n == 0 {
		return nil
	}
	widths := make([]int, n)
	for i := range widths {
		widths[i] = 1
	}
	for _, row := range rows {
		for i, cell := range row {
			if w := measure(cell); w > widths[i] {
				widths[i] = w
			}
		}
	}
	if width <= 0 {
		return widths
	}
	available := width - (3*n + 1)
	if available < n {
		available = n
	}
	for sumTableWidths(widths) > available {
		widest := -1
		for i, w := range widths {
			if w > 1 && (widest < 0 || w > widths[widest]) {
				widest = i
			}
		}
		if widest < 0 {
			break
		}
		widths[widest]--
	}
	return widths
}

func sumTableWidths(widths []int) int {
	total := 0
	for _, w := range widths {
		total += w
	}
	return total
}

func renderTableBlock(header []string, aligns []tableAlign, body [][]string, width int, measure func(string) int, breaker func(string, int) []string) []string {
	n := len(header)
	if len(aligns) < n {
		aligns = append(aligns, make([]tableAlign, n-len(aligns))...)
	}
	aligns = aligns[:n]

	rows := make([][]string, 0, len(body)+1)
	rows = append(rows, header)
	for _, row := range body {
		normalised := make([]string, n)
		copy(normalised, row)
		rows = append(rows, normalised)
	}
	widths := tableColumnWidths(rows, width, measure)

	// First pass over cells: wrap into column-sized segments and hard-truncate
	// any segment a narrow column still cannot hold (wide runes, fake measures).
	segments := make([][][]string, len(rows))
	for r, row := range rows {
		segments[r] = make([][]string, n)
		for c, cell := range row {
			parts := breaker(cell, widths[c])
			if len(parts) == 0 {
				parts = []string{""}
			}
			for i, part := range parts {
				// Wrap keeps the space that triggered a break on the next
				// segment; trim it so columns do not drift by one cell.
				part = strings.TrimSpace(part)
				if measure(part) > widths[c] {
					part = truncateTableCell(part, widths[c], measure)
				}
				parts[i] = part
			}
			segments[r][c] = parts
		}
	}

	var out []string
	for r := range rows {
		height := 0
		for c := range segments[r] {
			if len(segments[r][c]) > height {
				height = len(segments[r][c])
			}
		}
		for line := 0; line < height; line++ {
			cells := make([]string, n)
			for c := range cells {
				part := ""
				if line < len(segments[r][c]) {
					part = segments[r][c][line]
				}
				cells[c] = padTableCell(part, widths[c], aligns[c], measure)
			}
			out = append(out, joinTableCells(cells))
		}
		if r == 0 {
			divider := make([]string, n)
			for c := range divider {
				divider[c] = padTableCell(tableDividerCell(aligns[c], widths[c]), widths[c], tableAlignNone, measure)
			}
			out = append(out, joinTableCells(divider))
		}
	}
	return out
}

// cellClusters walks text one grapheme cluster at a time and reports the byte
// range [start, end) and the cluster's measured width. nextGrapheme is the same
// boundary authority used by TextWidth, Clip, Wrap and Buffer.WriteString, so a
// ZWJ emoji, a flag or a combining sequence stays intact when a cell is cut.
// Widths still go through the injected measure so custom width models keep
// working; only the cut points become cluster boundaries.
func cellClusters(text string, measure func(string) int, fn func(start, end int, cells int) bool) {
	for pos := 0; pos < len(text); {
		next, _ := nextGrapheme(text, pos)
		cluster := text[pos:next]
		if !fn(pos, next, measure(cluster)) {
			return
		}
		pos = next
	}
}

func truncateTableCell(text string, max int, measure func(string) int) string {
	if max < 1 {
		max = 1
	}
	if measure(text) <= max {
		return text
	}
	ellipsis := "…"
	budget := max - measure(ellipsis)
	if budget < 0 {
		budget = 0
	}
	var out strings.Builder
	used := 0
	cellClusters(text, measure, func(start, end int, cells int) bool {
		if used+cells > budget {
			return false
		}
		out.WriteString(text[start:end])
		used += cells
		return true
	})
	result := out.String() + ellipsis
	if measure(result) > max {
		// A measure that counts "…" wider than the column: drop the mark.
		return clipTableCell(text, max, measure)
	}
	return result
}

func clipTableCell(text string, max int, measure func(string) int) string {
	var out strings.Builder
	used := 0
	cellClusters(text, measure, func(start, end int, cells int) bool {
		if used+cells > max {
			return false
		}
		out.WriteString(text[start:end])
		used += cells
		return true
	})
	return out.String()
}

func padTableCell(text string, width int, align tableAlign, measure func(string) int) string {
	spare := width - measure(text)
	if spare <= 0 {
		return text
	}
	switch align {
	case tableAlignRight:
		return strings.Repeat(" ", spare) + text
	case tableAlignCenter:
		left := spare / 2
		return strings.Repeat(" ", left) + text + strings.Repeat(" ", spare-left)
	default:
		return text + strings.Repeat(" ", spare)
	}
}

func tableDividerCell(align tableAlign, width int) string {
	if width < 1 {
		width = 1
	}
	switch align {
	case tableAlignLeft:
		return ":" + strings.Repeat("-", width-1)
	case tableAlignRight:
		return strings.Repeat("-", width-1) + ":"
	case tableAlignCenter:
		if width < 2 {
			return ":"
		}
		return ":" + strings.Repeat("-", width-2) + ":"
	default:
		return strings.Repeat("-", width)
	}
}

func joinTableCells(cells []string) string {
	return "| " + strings.Join(cells, " | ") + " |"
}
