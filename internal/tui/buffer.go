package tui

import (
	"io"
	"strconv"
	"strings"
)

// Cell 是一个终端单元格。Text 保存绘制在该单元格的精确字节。
// 对于宽字素簇，第一个单元格持有整个簇（使
// ZWJ emoji、旗帜或组合序列完整到达终端），
// 第二个单元格的 Text 为空，渲染器会跳过它，因为
// 终端已经前进了两列。
type Cell struct {
	Text string
	S    Style
}

// Buffer 是单元格网格，它记住上次刷新的帧，使 Flush
// 只重写发生变化的行。重绘整行正是避免一行
// CJK 被局部重绘撕成两半的方法。
type Buffer struct {
	w, h  int
	cells []Cell
	prev  []Cell
	prevW int
	prevH int

	noColor bool // skip every SGR escape when the operator asked for --no-color
}

// NewBuffer 分配一个 w 列 h 行的缓冲区。
func NewBuffer(w, h int) *Buffer {
	b := &Buffer{prevW: -1, prevH: -1}
	b.Resize(w, h)
	return b
}

// Width 是缓冲区的列数。
func (b *Buffer) Width() int { return b.w }

// Height 是缓冲区的行数。
func (b *Buffer) Height() int { return b.h }

// Resize 改变网格，保留重叠的单元格。
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

// Clear 用空白单元格填充缓冲区。
func (b *Buffer) Clear() {
	for i := range b.cells {
		b.cells[i] = Cell{Text: " "}
	}
}

// Set 在 (x, y) 写入一个字素簇，当该簇为双宽时
// 预留其后的单元格。text 必须是 nextGrapheme 返回的
// 恰好一个簇；调用方通常通过 WriteString 进行。
func (b *Buffer) Set(x, y int, text string, style Style) {
	if x < 0 || y < 0 || y >= b.h || x >= b.w {
		return
	}
	width := TextWidth(text)
	if width == 0 {
		return
	}
	if x+width > b.w {
		b.cells[y*b.w+x] = Cell{Text: " ", S: style}
		return
	}
	b.cells[y*b.w+x] = Cell{Text: text, S: style}
	for i := 1; i < width; i++ {
		b.cells[y*b.w+x+i] = Cell{Text: "", S: style}
	}
}

// WriteString 在 (x, y) 写入 text，裁剪到 max 个单元格，并返回
// 使用的单元格数。它按字素簇遍历（与
// TextWidth/Wrap/Clip 采用同一权威），使 ZWJ emoji 或旗帜被绘制为一个字形，
// 并占据布局代码为其预留的相同单元格数。在这里按
// rune 遍历会丢掉 ZWJ 并高估簇的宽度。
func (b *Buffer) WriteString(x, y, max int, text string, style Style) int {
	used := 0
	for pos := 0; pos < len(text); {
		next, width := nextGrapheme(text, pos)
		if width == 0 {
			pos = next
			continue
		}
		if used+width > max {
			break
		}
		b.Set(x+used, y, text[pos:next], style)
		used += width
		pos = next
	}
	return used
}

// RowText 是某一行去掉尾随空白后的无样式内容。
// 宽 rune 的延续单元格被跳过，因此字符串的长度就是
// 可见内容的单元格宽度。
func (b *Buffer) RowText(y int) string {
	if y < 0 || y >= b.h {
		return ""
	}
	var out strings.Builder
	for x := 0; x < b.w; x++ {
		cell := b.cells[y*b.w+x]
		if cell.Text == "" {
			continue
		}
		out.WriteString(cell.Text)
	}
	return strings.TrimRight(out.String(), " ")
}

// Flush 写入与上次刷新帧的差异，并返回
// 写入的行数。尺寸变化会强制整屏重绘。
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
		writeRow(&frame, row, b.noColor)
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

// Invalidate 遗忘上一帧，使下一次 Flush 重绘所有内容。
func (b *Buffer) Invalidate() {
	b.prevW, b.prevH = -1, -1
	b.prev = nil
}

// SetNoColor 控制 Flush 是否发出 SGR 颜色转义。切换它会
// 强制整屏重绘，因为每个单元格的字节都变了。
func (b *Buffer) SetNoColor(v bool) {
	if b.noColor == v {
		return
	}
	b.noColor = v
	b.Invalidate()
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

// cursorTo 是移动到基于 1 的行列的 ANSI 绝对光标定位。
func cursorTo(row, col int) string {
	return "\x1b[" + strconv.Itoa(row) + ";" + strconv.Itoa(col) + "H"
}

// writeRow 输出一行，修剪尾随空白单元格，并用 EL 清除该行
// 其余部分。尾随空格正是让重绘无闪烁的原因。
func writeRow(out *strings.Builder, row []Cell, noColor bool) {
	end := len(row)
	for end > 0 {
		cell := row[end-1]
		if cell.S == (Style{}) && (cell.Text == " " || cell.Text == "") {
			end--
			continue
		}
		break
	}
	if noColor {
		// 连重置都不发：操作员要求零个 SGR 颜色序列。
		for _, cell := range row[:end] {
			if cell.Text != "" {
				out.WriteString(cell.Text)
			}
		}
		out.WriteString("\x1b[K")
		return
	}
	var current Style
	for _, cell := range row[:end] {
		if cell.Text == "" {
			continue
		}
		if cell.S != current {
			out.WriteString("\x1b[0m")
			out.WriteString(cell.S.SGR())
			current = cell.S
		}
		out.WriteString(cell.Text)
	}
	out.WriteString("\x1b[0m\x1b[K")
}
