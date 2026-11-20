package tui

import (
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Block prefixes. The assistant uses a hollow diamond and tools a filled
// circle, so the two never read as the same ink blot in a CJK font.
const (
	userMark         = "› "
	assistantMark    = "◆ "
	toolMark         = "● "
	toolCanceledMark = "⊘ "
	reasonMark       = "◇ "
	noticeMark       = "! "
)

const (
	maxBodyWidth = 120
	maxInputRows = 4

	// contextBarCells is the glyph count inside the brackets of the header's
	// context usage bar. Ten is compact yet still changes every ten percent.
	contextBarCells = 10
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
	panelX, panelW int // zero when no optional panel is visible
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
	if m.panelsVisible() {
		// The rail takes a third of the screen, up to a readable maximum. The
		// separator occupies panelX-1, so the transcript ends before it.
		panelW := minInt(36, maxInt(l.width/3, 20))
		if panelW > l.width-2 {
			panelW = maxInt(l.width-2, 1)
		}
		l.panelW = panelW
		l.panelX = l.width - panelW - 1
		bodyAvail := maxInt(l.panelX-2, 1)
		l.bodyW = minInt(bodyAvail, maxBodyWidth)
		l.bodyX = 1
	} else {
		l.bodyW = minInt(l.width, maxBodyWidth)
		l.bodyX = (l.width - l.bodyW) / 2
	}

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
	m.paintLine(buf, 1, ruleLine(width, m.palette.border()))

	body := m.bodyLinesLocked(l.bodyW)
	// Cache the geometry the input path uses to clamp scroll at the moment a
	// wheel/page key arrives, instead of letting overscroll accumulate between
	// paints and making a large transcript feel stuck at the top.
	m.bodyW = l.bodyW
	m.bodyLines = len(body)
	m.bodyVersion = m.version
	m.scroll = clampTranscriptScroll(m.scroll, len(body), l.bodyH)
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

	if l.panelW > 0 {
		m.paintPanels(buf, l)
	}

	if m.pending != nil {
		m.paintApproval(buf, l)
	}

	m.paintInput(buf, l)
	m.paintLine(buf, l.hintY, hintLine(width, m.locale, m.pending != nil, m.palette.dim()))
	m.paintLine(buf, l.statusY, m.statusLineLocked(width))
	m.paintSelectionLocked(buf)
	m.dirty = false
}

// paintSelectionLocked inverts the style of every cell in the current range.
// It runs after all other painting so header, transcript and input rows are
// highlighted uniformly, and it is a no-op when nothing is selected, keeping
// the default frame byte-for-byte unchanged.
func (m *Model) paintSelectionLocked(buf *Buffer) {
	if !m.hasSelectionLocked() || buf.Width() <= 0 || buf.Height() <= 0 {
		return
	}
	a, b := m.selectionRangeLocked()
	if a.Row >= buf.Height() || b.Row < 0 {
		return
	}
	w := buf.Width()
	for row := maxInt(a.Row, 0); row <= minInt(b.Row, buf.Height()-1); row++ {
		start, end := 0, w-1
		if row == a.Row {
			start = a.Col
		}
		if row == b.Row {
			end = b.Col
		}
		if start > end {
			continue
		}
		for col := maxInt(start, 0); col <= minInt(end, w-1); col++ {
			idx := row*w + col
			if buf.cells[idx].Text == "" {
				continue // second half of a wide grapheme cluster
			}
			style := buf.cells[idx].S
			style.Reverse = true
			buf.cells[idx].S = style
		}
	}
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

func ruleLine(width int, style Style) Line {
	line := NewLine()
	line.Push(strings.Repeat("─", maxInt(width, 1)), style)
	return line
}

// contextBar renders a 0-100 percentage as a bracketed bar of cells glyphs.
// The fraction is rounded half up; both █ and ░ are one terminal cell wide,
// so the result always occupies cells+2 cells regardless of the fill level.
func contextBar(pct int, cells int) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	if cells < 0 {
		cells = 0
	}
	filled := (pct*cells + 50) / 100
	if filled > cells {
		filled = cells
	}
	return "[" + strings.Repeat("█", filled) + strings.Repeat("░", cells-filled) + "]"
}

// contextStyle maps a context usage percentage onto an existing palette role:
// muted below half, warning through 85, and failure above it. Suppressed colour
// (--no-color or a degraded terminal) stays plain text, so no SGR escapes leak
// into the header where the rest of the row is already unstyled.
func (m *Model) contextStyle(pct int) Style {
	if m.noColor || m.degraded {
		return Style{}
	}
	switch {
	case pct > 85:
		return m.palette.failure()
	case pct >= 50:
		return m.palette.warning()
	default:
		return m.palette.muted()
	}
}

func (m *Model) headerLocked(width int) Line {
	dot := m.palette.muted()
	left := NewLine()
	left.Push("argent", m.palette.accent())
	if m.model != "" {
		left.Push(" · ", dot)
		left.Push(m.model, Style{})
	}
	if m.root != "" {
		left.Push(" · ", dot)
		left.Push(shortPath(m.root), m.palette.muted())
	}

	right := NewLine()
	first := true
	push := func(text string, style Style) {
		if text == "" {
			return
		}
		if !first {
			right.Push(" · ", dot)
		}
		first = false
		right.Push(text, style)
	}
	if m.usageIn+m.usageOut > 0 {
		push("↑"+compactCount(m.usageIn)+" ↓"+compactCount(m.usageOut), m.palette.muted())
	}
	if m.usageCached > 0 {
		push(text(m.locale, "cached")+" "+compactCount(m.usageCached), m.palette.muted())
	}
	if m.contextWindow > 0 {
		used := int64(m.usageIn) + int64(m.usageOut)
		pct := used * 100 / int64(m.contextWindow)
		if pct > 100 {
			pct = 100
		}
		push("ctx "+contextBar(int(pct), contextBarCells)+" "+strconv.FormatInt(pct, 10)+"%", m.contextStyle(int(pct)))
	}
	if m.cost != "" {
		push(m.cost, m.palette.muted())
	}
	push(clock(int(m.now().Sub(m.started).Seconds())), m.palette.muted())
	right.Push(" ", dot)

	room := width - right.Width() - 1
	if room < 4 {
		room = 4
	}
	return joinLine(clipLine(left, room), right, width)
}

func (m *Model) statusLineLocked(width int) Line {
	left := NewLine()
	message := ""
	style := m.palette.muted()
	now := m.now()
	if notice, ok := m.notices.Current(now); ok && notice.Text != "" {
		message = notice.Text
		switch notice.Severity {
		case SeverityError:
			style = m.palette.failure()
		case SeverityWarn, SeveritySysadmin:
			style = m.palette.warning()
		}
	} else if m.status != "" && now.Sub(m.statusAt) < 6*time.Second {
		// Legacy direct writers still render, but new notices always use the
		// stack above and take precedence when both are present.
		message = m.status
	} else if m.working {
		message = spinnerFrame(m.tickLocked()) + " " + text(m.locale, "working")
	} else {
		message = text(m.locale, "ready")
	}
	if message != "" {
		left.Push(Clip(message, width), style)
	}
	right := NewLine()
	var parts []string
	// The theme is worth a permanent reminder only when it differs from the
	// default; dark needs no label because it is the historic appearance.
	if m.theme != ThemeDark {
		parts = append(parts, text(m.locale, "theme")+" "+m.theme)
	}
	if m.scroll > 0 {
		parts = append(parts, "↑"+strconv.Itoa(m.scroll))
	}
	if m.usageIn+m.usageOut > 0 {
		parts = append(parts, compactCount(m.usageIn)+"/"+compactCount(m.usageOut))
	}
	if m.usageCached > 0 {
		parts = append(parts, text(m.locale, "cached")+" "+compactCount(m.usageCached))
	}
	if len(parts) > 0 {
		right.Push(strings.Join(parts, " · ")+" ", m.palette.muted())
	}
	return joinLine(left, right, width)
}

func (m *Model) tickLocked() int {
	return int(m.now().UnixMilli() / 120)
}

func (m *Model) paintInput(buf *Buffer, l layout) {
	border := m.palette.border()
	accent := m.palette.accent()

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
	border := m.palette.warning()

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
		out = append(out, row(label+segment, m.palette.muted()))
	}
	// A dangerous call shows why it was flagged and marks Deny as the
	// pre-selected default. Ordinary requests skip this block entirely, so the
	// modal above and the hint rows below are byte-for-byte unchanged for them.
	if danger := collapseSpace(m.pending.danger); danger != "" {
		label := "⚠ " + text(m.locale, "danger") + ": "
		continuation := strings.Repeat(" ", TextWidth(label))
		for i, segment := range Wrap(danger, maxInt(inner-TextWidth(label), 1)) {
			prefix := label
			if i > 0 {
				prefix = continuation
			}
			out = append(out, row(prefix+segment, m.palette.failure()))
		}
		out = append(out, row(text(m.locale, "default_deny"), m.palette.warning()))
	}
	out = append(out, row("", Style{}))
	out = append(out, row("[y] "+text(m.locale, "allow")+"   [n] "+text(m.locale, "deny"), Style{}))
	out = append(out, row("[a] "+text(m.locale, "allow_session"), m.palette.muted()))

	foot := "└" + strings.Repeat("─", maxInt(boxWidth-2, 0)) + "┘"
	out = append(out, PlainLine(Clip(foot, boxWidth)))
	return out
}

// entryCacheKey captures every input that can change how one entry renders.
// Concentrating it here keeps the cache honest when a new field is added to
// Entry: forgetting to add it to the key is a one-line fix in one place.
func (m *Model) entryCacheKeyLocked(entry Entry, width int) entryCacheKey {
	return entryCacheKey{
		kind:       entry.Kind,
		text:       entry.Text,
		name:       entry.Name,
		args:       entry.Args,
		output:     entry.Output,
		running:    entry.Running,
		failed:     entry.Failed,
		attempt:    entry.Attempt,
		width:      width,
		expanded:   m.expanded,
		noColor:    m.noColor,
		themeEpoch: m.themeEpoch,
		locale:     m.locale,
	}
}

// bodyLinesLocked renders the transcript, reusing cached lines for every
// entry whose key is unchanged. A streamed delta therefore repaints just one
// block, and a theme change invalidates all of them through themeEpoch.
func (m *Model) bodyLinesLocked(width int) []Line {
	m.flushStreamLocked()
	if len(m.entryCache) > len(m.entries) {
		m.entryCache = m.entryCache[:len(m.entries)]
	}
	out := make([]Line, 0, len(m.entries)*2)
	for i, entry := range m.entries {
		var lines []Line
		if entry.Kind == EntryReasoning || isLiveToolEntry(entry) {
			// The summary/spinner carries a live elapsed that changes without
			// any entry field changing, so never read it back from the cache.
			// A slot is still stored at index i to keep the cache aligned.
			lines = m.renderEntryLocked(entry, width)
		} else {
			key := m.entryCacheKeyLocked(entry, width)
			if i < len(m.entryCache) && m.entryCache[i].key == key {
				out = append(out, m.entryCache[i].lines...)
				continue
			}
			lines = m.renderEntryLocked(entry, width)
		}
		slot := entryCacheSlot{key: m.entryCacheKeyLocked(entry, width), lines: lines}
		if i < len(m.entryCache) {
			m.entryCache[i] = slot
		} else {
			m.entryCache = append(m.entryCache, slot)
		}
		out = append(out, lines...)
	}
	return out
}

func (m *Model) renderBodyLocked(width int) []Line {
	var out []Line
	for _, entry := range m.entries {
		out = append(out, m.renderEntryLocked(entry, width)...)
	}
	return out
}

// renderEntryLocked draws exactly one transcript block, including its attempt
// badge above it.
func (m *Model) renderEntryLocked(entry Entry, width int) []Line {
	var out []Line
	if entry.Attempt != "" {
		out = append(out, m.attemptLinesLocked(entry, width)...)
	}
	switch entry.Kind {
	case EntryUser:
		out = append(out, wrappedBlock(entry.Text, width, userMark, "  ", m.palette.user())...)
	case EntryAssistant:
		out = append(out, markdownLines(entry.Text, width, assistantMark, "  ", m.palette)...)
	case EntryReasoning:
		out = append(out, m.reasoningLines(entry, width)...)
	case EntryTool:
		if isLiveToolEntry(entry) {
			out = append(out, m.liveToolLines(entry, width)...)
		} else {
			out = append(out, toolLines(entry, width, m.expanded, m.palette)...)
		}
	case EntryNotice:
		out = append(out, wrappedBlock(entry.Text, width, noticeMark, "  ", m.palette.warning())...)
	}
	return out
}

// attemptLinesLocked draws the non-committed outcome badge above the row it
// belongs to. The wording is localised; the shape is stable for tests.
func (m *Model) attemptLinesLocked(entry Entry, width int) []Line {
	label := text(m.locale, "attempt_"+string(entry.Attempt))
	if label == "attempt_"+string(entry.Attempt) {
		label = string(entry.Attempt)
	}
	line := NewLine()
	line.Push("⟳ ", m.palette.warning())
	line.Push(label, m.palette.warning())
	if detail := collapseSpace(entry.Text); detail != "" {
		line.Push(" · "+Clip(detail, maxInt(width-TextWidth(label)-8, 1)), m.palette.muted())
	}
	return []Line{line}
}

// reasoningSummaryMaxCells caps the extracted phrase so a single thinking row
// stays compact; reasoningMinPhraseCells discards a too-short leading sentence
// ("Ok.") in favour of the following, more informative text.
const (
	reasoningSummaryMaxCells = 48
	reasoningMinPhraseCells  = 8
)

// thinkingLabel keeps the i18n lookup out of reasoningSummaryLine, whose first
// parameter deliberately shadows the package-level text() helper.
func thinkingLabel(locale string) string { return text(locale, "thinking") }

// reasoningPhrase flattens a reasoning block to one short key phrase: the first
// substantial sentence (or the whole flattened text when no sentence qualifies),
// clipped with an ellipsis to max cells. Truncate/Clip are grapheme-aware, so
// CJK text and emoji are never cut in the middle of a rune.
func reasoningPhrase(raw string, max int) string {
	flat := collapseSpace(raw)
	if flat == "" || max <= 0 {
		return ""
	}
	if end := strings.IndexAny(flat, ".!?。！？;；"); end > 0 {
		if first := flat[:end]; TextWidth(first) >= reasoningMinPhraseCells {
			flat = first
		}
	}
	return Truncate(flat, max)
}

// reasoningSummaryLine renders any amount of thinking text as exactly one row:
// a live row shows a spinner and an ellipsis, a completed row shows the elapsed
// duration. Fixed decoration is measured first so the phrase gets the cells
// that remain, and the whole row is clipped to width. The signature guarantees
// a single Line, so long reasoning can never reflow the transcript layout.
func reasoningSummaryLine(text string, elapsed time.Duration, width int, done bool, locale string, p Palette) Line {
	line := NewLine()

	spinner := ""
	if !done {
		// Deriving the frame from elapsed keeps this function pure (no tick
		// parameter) while still animating as the caller's clock advances.
		spinner = spinnerFrame(int(elapsed/(120*time.Millisecond))) + " "
	}
	tail := " …"
	if done {
		tail = " · " + formatDuration(elapsed)
	}

	label := thinkingLabel(locale)
	prefix := spinner + reasonMark + label + ":"
	budget := width - TextWidth(prefix) - TextWidth(tail)
	if budget < 0 {
		budget = 0
	}
	summary := reasoningPhrase(text, minInt(reasoningSummaryMaxCells, budget))

	if spinner != "" {
		line.Push(spinner, p.muted())
	}
	line.Push(reasonMark, p.reasoning())
	line.Push(label, p.muted())
	if summary != "" {
		line.Push(":", p.muted())
		line.Push(" ", Style{})
		line.Push(summary, p.dim())
	}
	line.Push(tail, p.muted())
	return clipLine(line, width)
}

func (m *Model) reasoningLines(entry Entry, width int) []Line {
	// Entry has no public timing field, so stamp private display timestamps on
	// first paint. The original reasoning text is never modified or dropped.
	idx := -1
	for i := len(m.entries) - 1; i >= 0; i-- {
		candidate := &m.entries[i]
		if candidate.Kind == EntryReasoning && candidate.Text == entry.Text {
			idx = i
			break
		}
	}
	// A trailing reasoning block is live only while a turn is still running;
	// once the agent moves on (or the turn ends) it shows its final duration.
	live := m.working && idx >= 0 && idx == len(m.entries)-1
	elapsed := time.Duration(0)
	if idx >= 0 {
		slot := &m.entries[idx]
		if slot.thinkingAt.IsZero() {
			slot.thinkingAt = m.now()
		}
		if live {
			slot.thinkingEnd = time.Time{}
			elapsed = m.now().Sub(slot.thinkingAt)
		} else {
			if slot.thinkingEnd.IsZero() {
				slot.thinkingEnd = m.now()
			}
			elapsed = slot.thinkingEnd.Sub(slot.thinkingAt)
		}
	}
	return []Line{reasoningSummaryLine(entry.Text, elapsed, width, !live, m.locale, m.palette)}
}

// --- streaming tool arguments ------------------------------------------------
//
// A tool call can arrive while its arguments JSON is still being streamed in.
// These helpers recover the top-level fields that are already complete so the
// pending row can show a useful preview instead of a half-finished JSON blob.
// They are pure and never error: malformed or partial input simply yields
// fewer (or no) fields.

// argField is one top-level field recovered from a streaming args JSON object.
type argField struct {
	Key   string
	Value string
}

// toolElapsedThreshold is the point at which a running tool's own elapsed time
// becomes more informative than the whole-turn clock in a pending row. Before
// it, the row shows the turn elapsed; past it, the row switches to the tool's
// own elapsed so a single slow call stands out.
const toolElapsedThreshold = 2 * time.Second

// partialArgsSummary extracts the complete top-level fields of a tool-args
// JSON object that may still be streaming in. A final string without its
// closing quote is salvaged with the text accumulated so far; an incomplete
// trailing escape or \uXXXX is dropped rather than leaking a replacement rune.
// Non-object, malformed and empty input yield no fields.
func partialArgsSummary(args string) []argField {
	i := skipJSONSpace(args, 0)
	if i >= len(args) || args[i] != '{' {
		return nil
	}
	i++ // consume '{'
	var out []argField
	for {
		i = skipJSONSpace(args, i)
		if i >= len(args) {
			break
		}
		switch args[i] {
		case '}':
			return out // complete object
		case ',':
			i++
			continue
		}
		if args[i] != '"' {
			return out // unexpected token: stop, keep what we already have
		}
		key, next, ok := scanJSONString(args, i)
		if !ok {
			return out // a key without a closing quote can never have a value
		}
		i = skipJSONSpace(args, next)
		if i >= len(args) || args[i] != ':' {
			return out
		}
		i = skipJSONSpace(args, i+1)
		if i >= len(args) {
			break // truncated right after the colon
		}
		value, next := scanJSONValue(args, i)
		out = append(out, argField{Key: key, Value: value})
		i = next
	}
	return out
}

func skipJSONSpace(s string, i int) int {
	for i < len(s) {
		switch s[i] {
		case ' ', '\t', '\n', '\r':
			i++
		default:
			return i
		}
	}
	return i
}

// scanJSONString decodes a JSON string starting at the opening quote. When the
// string is truncated (no closing quote yet) it returns the decoded prefix
// with complete=false; an incomplete trailing escape or \u escape is dropped.
func scanJSONString(s string, i int) (decoded string, next int, complete bool) {
	i++ // consume '"'
	var b strings.Builder
	for i < len(s) {
		c := s[i]
		if c == '"' {
			return b.String(), i + 1, true
		}
		if c != '\\' {
			b.WriteByte(c)
			i++
			continue
		}
		i++ // consume '\\'
		if i >= len(s) {
			return b.String(), i, false // truncated right after a backslash
		}
		switch s[i] {
		case '"', '\\', '/':
			b.WriteByte(s[i])
			i++
		case 'b':
			b.WriteByte('\b')
			i++
		case 'f':
			b.WriteByte('\f')
			i++
		case 'n':
			b.WriteByte('\n')
			i++
		case 'r':
			b.WriteByte('\r')
			i++
		case 't':
			b.WriteByte('\t')
			i++
		case 'u':
			r, after, ok := scanUnicodeEscape(s, i) // i is on 'u'
			if !ok {
				return b.String(), after, false // truncated/incomplete \u: drop it
			}
			appendValidRune(&b, r)
			i = after
		default:
			// Unknown escape: keep the literal byte (tolerant, never error).
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String(), i, false // EOF before the closing quote
}

// parseHex4 reads four hex digits following a 'u' at index u.
func parseHex4(s string, u int) (int, int, bool) {
	if u+5 > len(s) {
		return 0, u, false
	}
	value := 0
	for k := 1; k <= 4; k++ {
		c := s[u+k]
		var digit int
		switch {
		case c >= '0' && c <= '9':
			digit = int(c - '0')
		case c >= 'a' && c <= 'f':
			digit = int(c-'a') + 10
		case c >= 'A' && c <= 'F':
			digit = int(c-'A') + 10
		default:
			return 0, u + k, false
		}
		value = value*16 + digit
	}
	return value, u + 5, true
}

// scanUnicodeEscape decodes the \uXXXX at index u (pointing at 'u'), joining a
// high/low surrogate pair into one rune. An incomplete code point is dropped.
func scanUnicodeEscape(s string, u int) (rune, int, bool) {
	code, next, ok := parseHex4(s, u)
	if !ok {
		return 0, next, false
	}
	if code >= 0xDC00 && code <= 0xDFFF {
		return 0, next, false // lone low surrogate: drop
	}
	if code >= 0xD800 && code <= 0xDBFF {
		// A high surrogate needs a following \uXXXX low surrogate; while that
		// half is still streaming, drop the pair rather than show garbage.
		if next+6 <= len(s) && s[next] == '\\' && s[next+1] == 'u' {
			low, lowNext, ok := parseHex4(s, next+1)
			if ok && low >= 0xDC00 && low <= 0xDFFF {
				r := 0x10000 + rune(code-0xD800)*0x400 + rune(low-0xDC00)
				return r, lowNext, true
			}
		}
		return 0, next, false
	}
	return rune(code), next, true
}

func appendValidRune(b *strings.Builder, r rune) {
	if r < 0 || r > utf8.MaxRune || (r >= 0xD800 && r <= 0xDFFF) {
		return
	}
	b.WriteRune(r)
}

// scanJSONValue returns a short summary of the value at i and the index just
// after it. Strings decode escapes; objects/arrays collapse to "{…}"/"[…]";
// scalars keep their literal text. All paths advance i so the caller cannot
// loop forever on truncated input.
func scanJSONValue(s string, i int) (string, int) {
	if i >= len(s) {
		return "", i
	}
	switch s[i] {
	case '"':
		v, next, _ := scanJSONString(s, i)
		return v, next
	case '{', '[':
		return scanJSONContainer(s, i)
	default:
		j := i
		for j < len(s) && s[j] != ',' && s[j] != '}' && s[j] != ']' &&
			s[j] != ' ' && s[j] != '\t' && s[j] != '\n' && s[j] != '\r' {
			j++
		}
		if j == i {
			return "", i + 1 // unexpected delimiter: still make progress
		}
		return s[i:j], j
	}
}

// scanJSONContainer scans a balanced {...} or [...] with string awareness and
// returns a compact placeholder. A truncated container is still summarised so
// the field is not lost; the caller's field loop stops at EOF.
func scanJSONContainer(s string, i int) (string, int) {
	empty, nonempty := "{}", "{…}"
	if s[i] == '[' {
		empty, nonempty = "[]", "[…]"
	}
	depth, sawItem := 0, false
	for i < len(s) {
		c := s[i]
		if c == '"' {
			_, next, complete := scanJSONString(s, i)
			i = next
			if !complete {
				return nonempty, i
			}
			sawItem = true
			continue
		}
		switch c {
		case '{', '[':
			depth++
		case '}', ']':
			depth--
			if depth == 0 {
				if sawItem {
					return nonempty, i + 1
				}
				return empty, i + 1
			}
		default:
			if c != ' ' && c != '\t' && c != '\n' && c != '\r' && c != ':' && c != ',' {
				sawItem = true
			}
		}
		i++
	}
	if sawItem {
		return nonempty, i
	}
	return empty, i
}

// formatPartialArgs renders recovered fields as a compact "key=value" list,
// capped at three fields and 48 cells. Empty input yields an empty string.
func formatPartialArgs(args string) string {
	fields := partialArgsSummary(args)
	if len(fields) == 0 {
		return ""
	}
	parts := make([]string, 0, len(fields))
	for i, f := range fields {
		if i == 3 {
			parts = append(parts, "…")
			break
		}
		parts = append(parts, f.Key+"="+f.Value)
	}
	return Clip(strings.Join(parts, ", "), 48)
}

// adaptiveElapsed picks the elapsed shown by a pending row: the whole-turn
// clock until the tool's own elapsed passes the threshold, then the tool time.
func adaptiveElapsed(tool, turn, threshold time.Duration) time.Duration {
	if threshold <= 0 || tool >= threshold {
		return tool
	}
	return turn
}

// isLiveToolEntry reports whether an entry still needs a live (uncached) row
// because its spinner/elapsed changes without any entry field changing.
func isLiveToolEntry(entry Entry) bool {
	return entry.Kind == EntryTool && (entry.Running || entry.Attempt == AttemptCancelled)
}

// liveToolLines renders a running or cancelled tool entry with the streaming
// args preview. It reads the matching in-flight task (without mutating state)
// for tool-specific elapsed and uses the turn clock for whole-round elapsed.
// One entry always yields exactly one row.
func (m *Model) liveToolLines(entry Entry, width int) []Line {
	now := m.now()
	turn := time.Duration(0)
	if !m.started.IsZero() && now.After(m.started) {
		turn = now.Sub(m.started)
	}
	toolElapsed := time.Duration(0)
	for i := len(m.tasks) - 1; i >= 0; i-- {
		task := &m.tasks[i]
		if task.Name != entry.Name || (task.Status != TaskRunning && task.Status != TaskPending) {
			continue
		}
		if !task.Started.IsZero() && now.After(task.Started) {
			toolElapsed = now.Sub(task.Started)
		}
		break
	}
	cancelled := entry.Attempt == AttemptCancelled
	return []Line{toolPendingLine(entry.Name, entry.Args, toolElapsed, turn, width,
		cancelled || !entry.Running, entry.Failed, cancelled, m.locale, m.palette)}
}

// toolPendingLine renders one tool-call row. A live call animates a spinner and
// shows adaptive elapsed; a finished or failed call shows its outcome glyph and
// final duration; a cancelled call shows a cancelled glyph and label. The
// arguments are rendered from the possibly-streaming JSON via formatPartialArgs.
func toolPendingLine(name, args string, elapsed, turn time.Duration, width int, done, failed, cancelled bool, locale string, p Palette) Line {
	line := NewLine()
	glyph, style := toolMark, p.tool()
	spinner := ""
	tail := ""

	switch {
	case cancelled:
		glyph, style = toolCanceledMark, p.warning()
		tail = " · " + text(locale, "cancelled")
	case failed:
		glyph, style = "✖ ", p.failure()
		tail = " · " + formatDuration(elapsed)
	case done:
		tail = " · " + formatDuration(elapsed)
	default:
		glyph, style = "◐ ", p.warning()
		spinner = spinnerFrame(int(elapsed/(120*time.Millisecond))) + " "
		tail = " · " + formatDuration(adaptiveElapsed(elapsed, turn, toolElapsedThreshold)) + " …"
	}

	summary := formatPartialArgs(args)
	label := name
	if summary != "" {
		label += "(" + summary + ")"
	} else {
		label += "()"
	}

	budget := width - TextWidth(spinner) - TextWidth(glyph) - TextWidth(tail)
	if budget < 0 {
		budget = 0
	}
	if spinner != "" {
		line.Push(spinner, p.muted())
	}
	line.Push(glyph, style)
	line.Push(Clip(label, budget), Style{Bold: true})
	line.Push(tail, p.muted())
	return clipLine(line, width)
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
func hintLine(width int, locale string, modal bool, style Style) Line {
	hint := text(locale, "hint")
	if modal {
		hint = text(locale, "hint_modal")
	}
	line := NewLine()
	line.Push(ShortHelp(HintBindings(hint), width, TextWidth), style)
	return line
}
