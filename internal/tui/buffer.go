package tui

import (
	"io"
	"strconv"
	"strings"
)

// Cell is one terminal cell. R is 0 for the second half of a wide rune, which
// the renderer skips because the terminal already advanced two columns.
type Cell struct {
	R rune
	S Style
}

// Buffer is a grid of cells that remembers the last flushed frame so Flush
// only rewrites rows that changed. Redrawing a whole row is what keeps a line
// of CJK from being torn in half by a partial repaint.
type Buffer struct {
	w, h  int
	cells []Cell
	prev  []Cell
	prevW int
	prevH int
}

// NewBuffer allocates a buffer of w by h cells.
func NewBuffer(w, h int) *Buffer {
	b := &Buffer{prevW: -1, prevH: -1}
	b.Resize(w, h)
	return b
}

// Width is the buffer's column count.
func (b *Buffer) Width() int { return b.w }

// Height is the buffer's row count.
func (b *Buffer) Height() int { return b.h }

// Resize changes the grid, keeping the overlapping cells.
func (b *Buffer) Resize(w, h int) {
	if w < 0 {
		w = 0
	}
	if h < 0 {
		h = 0
	}
	if w == b.w && h == b.h && len(b.cells) == w*h {
		return
	}
	next := make([]Cell, w*h)
	for y := 0; y < h && y < b.h; y++ {
		for x := 0; x < w && x < b.w; x++ {
			next[y*w+x] = b.cells[y*b.w+x]
		}
	}
	b.w, b.h, b.cells = w, h, next
}

// Clear fills the buffer with blank cells.
func (b *Buffer) Clear() {
	for i := range b.cells {
		b.cells[i] = Cell{R: ' '}
	}
}

// Set writes one rune at (x, y), reserving the following cell when the rune is
// double width.
func (b *Buffer) Set(x, y int, r rune, style Style) {
	if x < 0 || y < 0 || y >= b.h || x >= b.w {
		return
	}
	width := RuneWidth(r)
	if width == 0 {
		return
	}
	if x+width > b.w {
		b.cells[y*b.w+x] = Cell{R: ' ', S: style}
		return
	}
	b.cells[y*b.w+x] = Cell{R: r, S: style}
	for i := 1; i < width; i++ {
		b.cells[y*b.w+x+i] = Cell{R: 0, S: style}
	}
}

// WriteString writes text at (x, y), clipped to max cells, and returns the
// number of cells used.
func (b *Buffer) WriteString(x, y, max int, text string, style Style) int {
	used := 0
	for _, r := range text {
		width := RuneWidth(r)
		if width == 0 {
			continue
		}
		if used+width > max {
			break
		}
		b.Set(x+used, y, r, style)
		used += width
	}
	return used
}

// RowText is the unstyled content of one row with trailing blanks trimmed.
// Wide-rune continuation cells are skipped, so the length of the string is the
// cell width of what is visible.
func (b *Buffer) RowText(y int) string {
	if y < 0 || y >= b.h {
		return ""
	}
	var out strings.Builder
	for x := 0; x < b.w; x++ {
		cell := b.cells[y*b.w+x]
		if cell.R == 0 {
			continue
		}
		out.WriteRune(cell.R)
	}
	return strings.TrimRight(out.String(), " ")
}

// Flush writes the diff against the previously flushed frame and returns the
// number of rows written. A size change forces a full repaint.
func (b *Buffer) Flush(out io.Writer) (int, error) {
	if b.w == 0 || b.h == 0 {
		return 0, nil
	}
	full := b.prevW != b.w || b.prevH != b.h || len(b.prev) != len(b.cells)

	var frame strings.Builder
	written := 0
	for y := 0; y < b.h; y++ {
		row := b.cells[y*b.w : (y+1)*b.w]
		if !full && rowEqual(row, b.prev[y*b.w:(y+1)*b.w]) {
			continue
		}
		frame.WriteString(cursorTo(y+1, 1))
		writeRow(&frame, row)
		written++
	}
	if written == 0 {
		return 0, nil
	}
	b.prev = append(b.prev[:0], b.cells...)
	b.prevW, b.prevH = b.w, b.h
	_, err := io.WriteString(out, frame.String())
	return written, err
}

// Invalidate forgets the previous frame, so the next Flush repaints everything.
func (b *Buffer) Invalidate() {
	b.prevW, b.prevH = -1, -1
	b.prev = nil
}

func rowEqual(a, b []Cell) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// cursorTo is the ANSI absolute cursor move to a 1-based row and column.
func cursorTo(row, col int) string {
	return "\x1b[" + strconv.Itoa(row) + ";" + strconv.Itoa(col) + "H"
}

// writeRow emits one row, trimming trailing blank cells and clearing the rest
// of the line with EL. Trailing spaces are what make a redraw flicker-free.
func writeRow(out *strings.Builder, row []Cell) {
	end := len(row)
	for end > 0 {
		cell := row[end-1]
		if cell.S == (Style{}) && (cell.R == ' ' || cell.R == 0) {
			end--
			continue
		}
		break
	}
	var current Style
	for _, cell := range row[:end] {
		if cell.R == 0 {
			continue
		}
		if cell.S != current {
			out.WriteString("\x1b[0m")
			out.WriteString(cell.S.SGR())
			current = cell.S
		}
		out.WriteRune(cell.R)
	}
	out.WriteString("\x1b[0m\x1b[K")
}
