package tui

import (
	"strings"
	"unicode/utf8"
)

// Editor 是提示缓冲区：文本、光标和历史环。
// 每次变更都让光标停留在 rune 边界，因此 CJK 输入绝不会
// 被切成两半。
type Editor struct {
	runes   []rune
	cursor  int
	history []string
	index   int    // -1 while editing a fresh line
	draft   string // text before history browsing started
}

// NewEditor 返回一个空的编辑器。
func NewEditor() Editor { return Editor{index: -1} }

// Text 是当前内容。
func (e *Editor) Text() string { return string(e.runes) }

// Runes 是当前内容的 rune 副本。
func (e *Editor) Runes() []rune { return append([]rune{}, e.runes...) }

// Cursor 是以 rune 索引表示的光标位置。
func (e *Editor) Cursor() int { return e.cursor }

// SetCursor 把光标设置到指定的 rune 索引，并钳制到 [0, len(e.runes)]。
// 它只改光标，不触碰文本、历史或浏览状态。vim motion 返回的是字节索引，
// 调用方先换算成 rune 索引再经由此方法定位光标，从而保证 CJK/emoji
// 不会被切成两半。零值与空缓冲区也安全：len(nil)==0，光标归零。
func (e *Editor) SetCursor(runeIdx int) {
	if runeIdx < 0 {
		runeIdx = 0
	} else if runeIdx > len(e.runes) {
		runeIdx = len(e.runes)
	}
	e.cursor = runeIdx
}

// SetText 替换内容并把光标放到末尾。
func (e *Editor) SetText(text string) {
	e.runes = []rune(text)
	e.cursor = len(e.runes)
	e.index = -1
}

// markEdited 退出历史浏览：一旦用户修改了召回的行，它就
// 是一个新的草稿，因此 HistoryNext 绝不能跳回失效的召回前
// 文本。load() 是唯一在 SetText 后有意设置 index 的路径。
func (e *Editor) markEdited() { e.index = -1 }

// Insert 在光标处写入文本。
func (e *Editor) Insert(text string) {
	if text == "" {
		return
	}
	insert := []rune(text)
	next := make([]rune, 0, len(e.runes)+len(insert))
	next = append(next, e.runes[:e.cursor]...)
	next = append(next, insert...)
	next = append(next, e.runes[e.cursor:]...)
	e.runes = next
	e.cursor += len(insert)
	e.markEdited()
}

// Backspace 删除光标前的 rune。
func (e *Editor) Backspace() bool {
	if e.cursor == 0 {
		return false
	}
	e.runes = append(e.runes[:e.cursor-1], e.runes[e.cursor:]...)
	e.cursor--
	e.markEdited()
	return true
}

// Delete 删除光标处的 rune。
func (e *Editor) Delete() bool {
	if e.cursor >= len(e.runes) {
		return false
	}
	e.runes = append(e.runes[:e.cursor], e.runes[e.cursor+1:]...)
	e.markEdited()
	return true
}

// Left 把光标向后移动一个 rune。
func (e *Editor) Left() {
	if e.cursor > 0 {
		e.cursor--
	}
}

// Right 把光标向前移动一个 rune。
func (e *Editor) Right() {
	if e.cursor < len(e.runes) {
		e.cursor++
	}
}

// Home 把光标放到缓冲区开头。
func (e *Editor) Home() { e.cursor = 0 }

// End 把光标放到缓冲区末尾。
func (e *Editor) End() { e.cursor = len(e.runes) }

// KillToEnd 从光标删除到缓冲区末尾。
func (e *Editor) KillToEnd() bool {
	if e.cursor >= len(e.runes) {
		return false
	}
	e.runes = e.runes[:e.cursor]
	e.markEdited()
	return true
}

// KillToStart 从缓冲区开头删除到光标。
func (e *Editor) KillToStart() bool {
	if e.cursor == 0 {
		return false
	}
	e.runes = append([]rune{}, e.runes[e.cursor:]...)
	e.cursor = 0
	e.markEdited()
	return true
}

// KillWord 删除光标前的词，先跳过尾随空格。
func (e *Editor) KillWord() bool {
	if e.cursor == 0 {
		return false
	}
	start := e.cursor
	for start > 0 && e.runes[start-1] == ' ' {
		start--
	}
	for start > 0 && e.runes[start-1] != ' ' && e.runes[start-1] != '\n' {
		start--
	}
	if start == e.cursor {
		return false
	}
	e.runes = append(e.runes[:start], e.runes[e.cursor:]...)
	e.cursor = start
	e.markEdited()
	return true
}

// Clear 清空缓冲区并退出历史浏览。
func (e *Editor) Clear() {
	e.runes = nil
	e.cursor = 0
	e.index = -1
	e.draft = ""
}

// Record 把提交的行加入历史，忽略连续的重复。
func (e *Editor) Record(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	if n := len(e.history); n > 0 && e.history[n-1] == text {
		return
	}
	e.history = append(e.history, text)
	if len(e.history) > 200 {
		e.history = e.history[len(e.history)-200:]
	}
}

// HistoryPrev 在历史中后退一步。
func (e *Editor) HistoryPrev() bool {
	if len(e.history) == 0 {
		return false
	}
	next := e.index
	switch {
	case next == -1:
		e.draft = e.Text()
		next = len(e.history) - 1
	case next > 0:
		next--
	default:
		return false
	}
	e.load(e.history[next], next)
	return true
}

// HistoryNext 在历史中前进一步，回到末尾的草稿。
func (e *Editor) HistoryNext() bool {
	if e.index == -1 {
		return false
	}
	next := e.index + 1
	if next >= len(e.history) {
		e.load(e.draft, -1)
		return true
	}
	e.load(e.history[next], next)
	return true
}

// load 替换缓冲区但不退出历史浏览。
func (e *Editor) load(text string, index int) {
	e.SetText(text)
	e.index = index
}

// MoveLine 把光标上下移动一个逻辑行。当缓冲区只有一行
// 或光标已在边缘时返回 false，此时
// 调用方应回退到历史。
func (e *Editor) MoveLine(delta int) bool {
	hasNewline := false
	for _, r := range e.runes {
		if r == '\n' {
			hasNewline = true
			break
		}
	}
	if !hasNewline {
		return false
	}
	lineStart := e.cursor
	for lineStart > 0 && e.runes[lineStart-1] != '\n' {
		lineStart--
	}
	column := e.cursor - lineStart
	if delta < 0 {
		if lineStart == 0 {
			return false
		}
		prevEnd := lineStart - 1
		prevStart := prevEnd
		for prevStart > 0 && e.runes[prevStart-1] != '\n' {
			prevStart--
		}
		if column > prevEnd-prevStart {
			column = prevEnd - prevStart
		}
		e.cursor = prevStart + column
		return true
	}
	lineEnd := e.cursor
	for lineEnd < len(e.runes) && e.runes[lineEnd] != '\n' {
		lineEnd++
	}
	if lineEnd >= len(e.runes) {
		return false
	}
	nextStart := lineEnd + 1
	nextEnd := nextStart
	for nextEnd < len(e.runes) && e.runes[nextEnd] != '\n' {
		nextEnd++
	}
	if column > nextEnd-nextStart {
		column = nextEnd - nextStart
	}
	e.cursor = nextStart + column
	return true
}

// inputRow 是输入框中一个显示行。
type inputRow struct {
	text  string
	start int // rune index in the buffer where the row begins
}

// wrapInput 把缓冲区折叠为宽度不超过 width 个单元格的行。断点落在
// 字素边界上（与 Wrap 相同的规则）：ZWJ emoji、旗帜对或
// 组合序列是一个可见字形，绝不能被拆到两
// 行。一行的起点是它包含的第一个簇的 rune 索引，这使
// rowCaretColumn 与实际绘制的内容保持同步。
func wrapInput(runes []rune, width int) []inputRow {
	if width < 1 {
		width = 1
	}
	text := string(runes)
	rows := make([]inputRow, 0, 1)
	lineStart, lineStartRune := 0, 0
	bytePos, runePos, used := 0, 0, 0
	for bytePos < len(text) {
		if text[bytePos] == '\n' {
			rows = append(rows, inputRow{text: text[lineStart:bytePos], start: lineStartRune})
			bytePos++
			runePos++
			lineStart, lineStartRune, used = bytePos, runePos, 0
			continue
		}
		end, cell := nextGrapheme(text, bytePos)
		if used > 0 && used+cell > width {
			rows = append(rows, inputRow{text: text[lineStart:bytePos], start: lineStartRune})
			lineStart, lineStartRune, used = bytePos, runePos, 0
		}
		runePos += utf8.RuneCountInString(text[bytePos:end])
		used += cell
		bytePos = end
	}
	rows = append(rows, inputRow{text: text[lineStart:], start: lineStartRune})
	return rows
}

// inputWindow 让光标所在行保持可见，最多显示 maxRows 行。
func inputWindow(rows []inputRow, cursor, maxRows int) ([]inputRow, int) {
	if maxRows < 1 {
		maxRows = 1
	}
	cursorRow := 0
	for i, row := range rows {
		if row.start <= cursor {
			cursorRow = i
		}
	}
	offset := 0
	if cursorRow >= maxRows {
		offset = cursorRow + 1 - maxRows
	}
	if limit := len(rows) - maxRows; offset > limit {
		offset = limit
	}
	if offset < 0 {
		offset = 0
	}
	end := offset + maxRows
	if end > len(rows) {
		end = len(rows)
	}
	return rows[offset:end], cursorRow - offset
}

// rowCaretColumn 是光标在其行内的单元格偏移。
func rowCaretColumn(row inputRow, runes []rune, cursor int) int {
	if cursor > len(runes) {
		cursor = len(runes)
	}
	if cursor < row.start {
		return 0
	}
	return TextWidth(string(runes[row.start:cursor]))
}
