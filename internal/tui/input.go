package tui

import (
	"strings"
	"unicode/utf8"
)

// Editor is the prompt buffer: the text, the cursor and the history ring.
// Every mutation keeps the cursor on a rune boundary, so CJK input is never
// split in half.
type Editor struct {
	runes   []rune
	cursor  int
	history []string
	index   int    // -1 while editing a fresh line
	draft   string // text before history browsing started
}

// NewEditor returns an empty editor.
func NewEditor() Editor { return Editor{index: -1} }

// Text is the current content.
func (e *Editor) Text() string { return string(e.runes) }

// Runes is a copy of the current content as runes.
func (e *Editor) Runes() []rune { return append([]rune{}, e.runes...) }

// Cursor is the caret position as a rune index.
func (e *Editor) Cursor() int { return e.cursor }

// SetText replaces the content and puts the caret at the end.
func (e *Editor) SetText(text string) {
	e.runes = []rune(text)
	e.cursor = len(e.runes)
	e.index = -1
}

// markEdited leaves history browsing: once the user changes a recalled line it
// is a fresh draft, so HistoryNext must not snap back to the stale pre-recall
// text. load() is the only path that deliberately sets index after a SetText.
func (e *Editor) markEdited() { e.index = -1 }

// Insert writes text at the caret.
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

// Backspace removes the rune before the caret.
func (e *Editor) Backspace() bool {
	if e.cursor == 0 {
		return false
	}
	e.runes = append(e.runes[:e.cursor-1], e.runes[e.cursor:]...)
	e.cursor--
	e.markEdited()
	return true
}

// Delete removes the rune at the caret.
func (e *Editor) Delete() bool {
	if e.cursor >= len(e.runes) {
		return false
	}
	e.runes = append(e.runes[:e.cursor], e.runes[e.cursor+1:]...)
	e.markEdited()
	return true
}

// Left moves the caret one rune back.
func (e *Editor) Left() {
	if e.cursor > 0 {
		e.cursor--
	}
}

// Right moves the caret one rune forward.
func (e *Editor) Right() {
	if e.cursor < len(e.runes) {
		e.cursor++
	}
}

// Home puts the caret at the start of the buffer.
func (e *Editor) Home() { e.cursor = 0 }

// End puts the caret at the end of the buffer.
func (e *Editor) End() { e.cursor = len(e.runes) }

// KillToEnd deletes from the caret to the end of the buffer.
func (e *Editor) KillToEnd() bool {
	if e.cursor >= len(e.runes) {
		return false
	}
	e.runes = e.runes[:e.cursor]
	e.markEdited()
	return true
}

// KillToStart deletes from the start of the buffer to the caret.
func (e *Editor) KillToStart() bool {
	if e.cursor == 0 {
		return false
	}
	e.runes = append([]rune{}, e.runes[e.cursor:]...)
	e.cursor = 0
	e.markEdited()
	return true
}

// KillWord deletes the word before the caret, skipping trailing spaces first.
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

// Clear empties the buffer and leaves history browsing.
func (e *Editor) Clear() {
	e.runes = nil
	e.cursor = 0
	e.index = -1
	e.draft = ""
}

// Record adds a submitted line to the history, ignoring consecutive repeats.
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

// HistoryPrev walks one step back in the history.
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

// HistoryNext walks one step forward, back to the draft at the end.
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

// load replaces the buffer without leaving history browsing.
func (e *Editor) load(text string, index int) {
	e.SetText(text)
	e.index = index
}

// MoveLine moves the caret one logical line up or down. It reports false when
// the buffer has a single line or the caret already sits on the edge, which is
// when the caller should fall back to history.
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

// inputRow is one displayed row of the input box.
type inputRow struct {
	text  string
	start int // rune index in the buffer where the row begins
}

// wrapInput folds the buffer into rows no wider than width cells. Breaks land
// on grapheme boundaries (the same rule as Wrap): a ZWJ emoji, a flag pair or a
// combining sequence is one visible glyph and must never be split across two
// rows. A row's start is the rune index of the first cluster it contains, which
// keeps rowCaretColumn in sync with what is actually painted.
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

// inputWindow keeps the caret row visible, showing at most maxRows rows.
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

// rowCaretColumn is the cell offset of the caret inside its row.
func rowCaretColumn(row inputRow, runes []rune, cursor int) int {
	if cursor > len(runes) {
		cursor = len(runes)
	}
	if cursor < row.start {
		return 0
	}
	return TextWidth(string(runes[row.start:cursor]))
}
