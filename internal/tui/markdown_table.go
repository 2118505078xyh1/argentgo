package tui

import "strings"

// 本文件刻意自包含：它添加 markdown 表格渲染
// 而不触碰 markdown.go。主入口接受 textWidth 和 wrap
// 作为注入函数，使布局可在没有 Palette 的情况下测试；传入
// nil 则选用本包真实的 TextWidth/Wrap 实现。

type tableAlign uint8

const (
	tableAlignNone   tableAlign = iota // no colon in the delimiter cell
	tableAlignLeft                     // :---
	tableAlignCenter                   // :---:
	tableAlignRight                    // ---:
)

// renderMarkdownTable 把 lines 中每个被识别的 markdown 表格块替换
// 为对齐的行。其他所有内容原样复制。当某行含 '|' 且紧随其后的一行是
// 仅由 '-' 以及每格可选的前导/尾随 ':' 构成的分隔符行时，
// 该块被识别，例如 |---|:---:|---:|。其后含 '|' 的行是正文行；
// 第一个不含 '|' 的行
// 结束该块。
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

// splitTableRow 按 '|' 切分并丢弃由前导或尾随竖线产生的空端，
// 然后修剪每个单元格。内部空单元格被保留。
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

// parseTableDelimiter 只接受至少三个破折号、且最多一个前导和一个尾随冒号的单元格。
// 这种严格性使普通的
// 两列行不会被误认为分隔符。
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

// tableColumnWidths 测量每个单元格，然后每次把最宽的列缩小
// 一个单元格，直到整行放得进 width。一行占用 3*n+1 个单元格的竖线
// 和空格开销："| c0 | c1 | ... |"。
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

	// 第一遍遍历单元格：折叠成列大小的片段，并硬截断
	// 窄列仍放不下的任何片段（宽 rune、伪造的测量值）。
	segments := make([][][]string, len(rows))
	for r, row := range rows {
		segments[r] = make([][]string, n)
		for c, cell := range row {
			parts := breaker(cell, widths[c])
			if len(parts) == 0 {
				parts = []string{""}
			}
			for i, part := range parts {
				// Wrap 会把触发断行的空格保留在下一个
				// 片段；修剪它，使列不会偏移一个单元格。
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

// cellClusters 逐个字素簇遍历 text，报告字节
// 范围 [start, end) 和该簇的测量宽度。nextGrapheme 与
// TextWidth、Clip、Wrap 和 Buffer.WriteString 使用同一边界权威，因此
// 单元格被切割时 ZWJ emoji、旗帜或组合序列保持完整。
// 宽度仍通过注入的 measure 计算，使自定义宽度模型继续
// 工作；只有切割点变成簇边界。
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
		// 一个把 "…" 测量得比列还宽的 measure：丢弃该标记。
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
