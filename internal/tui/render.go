package tui

import (
	"strconv"
	"strings"
	"time"
)

// Block prefixes. The assistant uses a hollow diamond and tools a filled
// circle, so the two never read as the same ink blot in a CJK font.
const (
	userMark      = "› "
	assistantMark = "◆ "
	toolMark      = "● "
	reasonMark    = "◇ "
	noticeMark    = "! "
)

const (
	maxBodyWidth = 120
	maxInputRows = 4
)

var spinnerFrames = [...]string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func spinnerFrame(tick int) string {
	if tick < 0 {
		tick = -tick
	}
	return spinnerFrames[tick%len(spinnerFrames)]
}

// layout is the frame geometry, shared by the painter and the caret query so
// the two can never disagree.
type layout struct {
	width, height  int
	bodyX, bodyW   int
	bodyTop, bodyH int
	inputTop       int
	hintY, statusY int
	rows           []inputRow
	cursorRow      int
	cursorCol      int
}

func (m *Model) layoutLocked(width, height int) layout {
	l := layout{width: width, height: height}
	if l.width < 1 {
		l.width = 1
	}
	if l.height < 1 {
		l.height = 1
	}
	l.bodyW = minInt(l.width, maxBodyWidth)
	l.bodyX = (l.width - l.bodyW) / 2

	runes := m.editor.Runes()
	// "│ " + mark (2) + text + " │" leaves width-6 cells for the text.
	wrapped := wrapInput(runes, maxInt(l.width-6, 1))
	visible, cursorRow := inputWindow(wrapped, m.editor.Cursor(), maxInputRows)
	l.rows = visible
	l.cursorRow = cursorRow
	if cursorRow >= 0 && cursorRow < len(visible) {
		l.cursorCol = rowCaretColumn(visible[cursorRow], runes, m.editor.Cursor())
	}
	l.statusY = l.height - 1
	l.hintY = l.height - 2
	l.inputTop = l.height - 2 - (len(visible) + 2)
	l.bodyTop = 2
	l.bodyH = l.inputTop - l.bodyTop
	if l.bodyH < 1 {
		l.bodyH = 1
	}
	return l
}

// Frame renders the model into a fresh buffer.
func (m *Model) Frame(width, height int) *Buffer {
	buf := NewBuffer(width, height)
	m.Paint(buf)
	return buf
}

// Paint draws the model into an existing buffer. The buffer remembers the last
// frame, so Flush only rewrites rows that changed.
func (m *Model) Paint(buf *Buffer) {
	m.mu.Lock()
	defer m.mu.Unlock()

	width, height := buf.Width(), buf.Height()
	if width < 1 || height < 1 {
		return
	}
	buf.Clear()
	l := m.layoutLocked(width, height)
	m.bodyH = l.bodyH

	m.paintLine(buf, 0, m.headerLocked(width))
	m.paintLine(buf, 1, ruleLine(width))

	body := m.bodyLinesLocked(l.bodyW)
	maxScroll := len(body) - l.bodyH
	if maxScroll < 0 {
		maxScroll = 0
	}
	if m.scroll > maxScroll {
		m.scroll = maxScroll
	}
	end := len(body) - m.scroll
	if end > len(body) {
		end = len(body)
	}
	start := end - l.bodyH
	if start < 0 {
		start = 0
	}
	for i := 0; i < l.bodyH; i++ {
		index := start + i
		if index >= end || index >= len(body) {
			break
		}
		m.paintLineAt(buf, l.bodyTop+i, l.bodyX, body[index])
	}

	if m.pending != nil {
		m.paintApproval(buf, l)
	}

	m.paintInput(buf, l)
	m.paintLine(buf, l.hintY, hintLine(width, m.locale, m.pending != nil))
	m.paintLine(buf, l.statusY, m.statusLineLocked(width))
}

func (m *Model) paintLine(buf *Buffer, y int, line Line) {
	m.paintLineAt(buf, y, 0, line)
}

func (m *Model) paintLineAt(buf *Buffer, y, x int, line Line) {
	if y < 0 || y >= buf.Height() || x >= buf.Width() {
		return
	}
	column := x
	for _, span := range line.Spans {
		remaining := buf.Width() - column
		if remaining <= 0 {
			return
		}
		column += buf.WriteString(column, y, remaining, span.Text, span.Style)
	}
}

func ruleLine(width int) Line {
	line := NewLine()
	line.Push(strings.Repeat("─", maxInt(width, 1)), Style{Fg: ColorGray})
	return line
}

func (m *Model) headerLocked(width int) Line {
	dot := Style{Fg: ColorGray}
	left := NewLine()
	left.Push("argent", Style{Fg: ColorCyan, Bold: true})
	if m.model != "" {
		left.Push(" · ", dot)
		left.Push(m.model, Style{})
	}
	if m.root != "" {
		left.Push(" · ", dot)
		left.Push(shortPath(m.root), Style{Fg: ColorGray})
	}

	var parts []string
	if m.usageIn+m.usageOut > 0 {
		parts = append(parts, "↑"+compactCount(m.usageIn)+" ↓"+compactCount(m.usageOut))
	}
	parts = append(parts, clock(int(m.now().Sub(m.started).Seconds())))
	right := NewLine()
	right.Push(strings.Join(parts, " · ")+" ", Style{Fg: ColorGray})

	room := width - right.Width() - 1
	if room < 4 {
		room = 4
	}
	return joinLine(clipLine(left, room), right, width)
}

func (m *Model) statusLineLocked(width int) Line {
	left := NewLine()
	message := ""
	if m.status != "" && m.now().Sub(m.statusAt) < 6*time.Second {
		message = m.status
	} else if m.working {
		message = spinnerFrame(m.tickLocked()) + " " + text(m.locale, "working")
	} else {
		message = text(m.locale, "ready")
	}
	if message != "" {
		left.Push(Clip(message, width), Style{Fg: ColorGray})
	}
	right := NewLine()
	var parts []string
	if m.scroll > 0 {
		parts = append(parts, "↑"+strconv.Itoa(m.scroll))
	}
	if m.usageIn+m.usageOut > 0 {
		parts = append(parts, compactCount(m.usageIn)+"/"+compactCount(m.usageOut))
	}
	if len(parts) > 0 {
		right.Push(strings.Join(parts, " · ")+" ", Style{Fg: ColorGray})
	}
	return joinLine(left, right, width)
}

func (m *Model) tickLocked() int {
	return int(m.now().UnixMilli() / 120)
}

func (m *Model) paintInput(buf *Buffer, l layout) {
	border := Style{Fg: ColorGray}
	accent := Style{Fg: ColorCyan, Bold: true}

	top := NewLine()
	top.Push("╭"+strings.Repeat("─", maxInt(l.width-2, 0))+"╮", border)
	m.paintLine(buf, l.inputTop, top)

	for i, row := range l.rows {
		mark := "  "
		if i == 0 && row.start == 0 {
			mark = userMark
		}
		line := NewLine()
		line.Push("│ ", border)
		line.Push(mark, accent)
		line.Push(Clip(row.text, maxInt(l.width-6, 1)), Style{})
		line.Fill(maxInt(l.width-2, 1), Style{})
		line.Push(" │", border)
		m.paintLine(buf, l.inputTop+1+i, line)
	}

	bottom := NewLine()
	bottom.Push("╰"+strings.Repeat("─", maxInt(l.width-2, 0))+"╯", border)
	m.paintLine(buf, l.inputTop+1+len(l.rows), bottom)
}

// paintApproval draws the modal over a blanked band so the transcript behind
// it never shows through the box margins.
func (m *Model) paintApproval(buf *Buffer, l layout) {
	lines := m.approvalLines(l.bodyW)
	if len(lines) == 0 {
		return
	}
	boxWidth := lines[0].Width()
	top := l.bodyTop + (l.bodyH-len(lines))/2
	if top < l.bodyTop {
		top = l.bodyTop
	}
	x := l.bodyX + (l.bodyW-boxWidth)/2
	blank := PlainLine(strings.Repeat(" ", l.width))
	for i := range lines {
		m.paintLineAt(buf, top+i, 0, blank)
	}
	for i, line := range lines {
		m.paintLineAt(buf, top+i, x, line)
	}
}

// approvalLines draws the modal that blocks input until y / n / a is pressed.
func (m *Model) approvalLines(width int) []Line {
	if m.pending == nil {
		return nil
	}
	boxWidth := minInt(maxInt(width-4, 24), 64)
	inner := boxWidth - 4
	border := Style{Fg: ColorYellow}

	out := make([]Line, 0, 8)
	title := text(m.locale, "approval_title")
	head := "┌─ " + title + " "
	head += strings.Repeat("─", maxInt(boxWidth-TextWidth(head)-1, 0)) + "┐"
	out = append(out, PlainLine(Clip(head, boxWidth)))

	row := func(content string, style Style) Line {
		line := NewLine()
		line.Push("│ ", border)
		line.Push(Pad(content, inner), style)
		line.Push(" │", border)
		return line
	}
	out = append(out, row(text(m.locale, "tool")+": "+m.pending.name, Style{Bold: true}))

	args := collapseSpace(m.pending.args)
	if args == "" {
		args = "{}"
	}
	for i, segment := range Wrap(args, maxInt(inner-6, 1)) {
		label := "args: "
		if i > 0 {
			label = "      "
		}
		out = append(out, row(label+segment, Style{Fg: ColorGray}))
	}
	out = append(out, row("", Style{}))
	out = append(out, row("[y] "+text(m.locale, "allow")+"   [n] "+text(m.locale, "deny"), Style{}))
	out = append(out, row("[a] "+text(m.locale, "allow_session"), Style{Fg: ColorGray}))

	foot := "└" + strings.Repeat("─", maxInt(boxWidth-2, 0)) + "┘"
	out = append(out, PlainLine(Clip(foot, boxWidth)))
	return out
}

// bodyLinesLocked renders the transcript and caches it until something that
// affects the body changes.
func (m *Model) bodyLinesLocked(width int) []Line {
	m.flushStreamLocked()
	if cache := m.cache; cache != nil && cache.width == width && cache.version == m.version && cache.expanded == m.expanded {
		return cache.lines
	}
	lines := m.renderBodyLocked(width)
	m.cache = &bodyCache{width: width, version: m.version, expanded: m.expanded, lines: lines}
	return lines
}

func (m *Model) renderBodyLocked(width int) []Line {
	var out []Line
	for _, entry := range m.entries {
		switch entry.Kind {
		case EntryUser:
			out = append(out, wrappedBlock(entry.Text, width, userMark, "  ", Style{Fg: ColorCyan, Bold: true})...)
		case EntryAssistant:
			out = append(out, markdownLines(entry.Text, width, assistantMark, "  ")...)
		case EntryReasoning:
			out = append(out, m.reasoningLines(entry, width)...)
		case EntryTool:
			out = append(out, toolLines(entry, width, m.expanded)...)
		case EntryNotice:
			out = append(out, wrappedBlock(entry.Text, width, noticeMark, "  ", Style{Fg: ColorYellow})...)
		}
	}
	return out
}

func (m *Model) reasoningLines(entry Entry, width int) []Line {
	head := NewLine()
	head.Push(reasonMark, Style{Fg: ColorMagenta})
	head.Push(text(m.locale, "thinking"), Style{Fg: ColorGray})
	out := []Line{head}
	dim := Style{Fg: ColorGray, Dim: true}
	out = append(out, wrappedBlock(entry.Text, width, "  ", "  ", dim)...)
	return out
}

func shortPath(path string) string {
	if home, err := homeDir(); err == nil && home != "" && strings.HasPrefix(path, home) {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}

func joinLine(left, right Line, width int) Line {
	if right.Width() == 0 {
		left.Fill(width, Style{})
		return left
	}
	gap := width - left.Width() - right.Width()
	if gap < 1 {
		gap = 1
	}
	left.Push(strings.Repeat(" ", gap), Style{})
	for _, span := range right.Spans {
		left.PushSpan(span)
	}
	return left
}

func clipLine(line Line, width int) Line {
	out := NewLine()
	used := 0
	for _, span := range line.Spans {
		if used >= width {
			break
		}
		clipped := Clip(span.Text, width-used)
		out.Push(clipped, span.Style)
		used += TextWidth(clipped)
	}
	return out
}

// hintLine is the footer key bar.
func hintLine(width int, locale string, modal bool) Line {
	hint := text(locale, "hint")
	if modal {
		hint = text(locale, "hint_modal")
	}
	line := NewLine()
	line.Push(Clip(hint, width), Style{Fg: ColorGray, Dim: true})
	return line
}
