package tui

import (
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// 块前缀。助手使用空心菱形，工具使用实心
// 圆，使两者在 CJK 字体中不会被读成同一个墨点。
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

	// contextBarCells 是头部上下文用量条括号内的字形数。
	// 十是紧凑的，但仍每百分之十变化一次。
	contextBarCells = 10
)

var spinnerFrames = [...]string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func spinnerFrame(tick int) string {
	if tick < 0 {
		tick = -tick
	}
	return spinnerFrames[tick%len(spinnerFrames)]
}

// layout 是帧的几何，绘制器与光标查询共享它，使
// 两者永不一致。
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
		// 侧边栏占屏幕三分之一，上限为易读的最大值。
		// 分隔符占据 panelX-1，因此记录在它之前结束。
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
	// "│ " + 标记 (2) + 文本 + " │" 为文本留下 width-6 个单元格。
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

// Frame 把模型渲染到一个新的缓冲区。
func (m *Model) Frame(width, height int) *Buffer {
	buf := NewBuffer(width, height)
	m.Paint(buf)
	return buf
}

// Paint 把模型绘制到已有缓冲区。缓冲区记住上一
// 帧，因此 Flush 只重写变化的行。
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
	// 缓存输入路径在滚轮/翻页按键到达时用于钳制滚动的几何，
	// 而不是让过度滚动在两次绘制之间累积，
	// 使长记录感觉卡在顶部。
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

// paintSelectionLocked 反转当前范围内每个单元格的样式。
// 它在所有其他绘制之后运行，使头部、记录和输入行
// 被统一高亮，且没有选择时为空操作，保持
// 默认帧逐字节不变。
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

// contextBar 把 0-100 的百分比渲染为带括号的单元格字形条。
// 分数四舍五入；█ 和 ░ 都恰好占一个终端单元格宽，
// 因此无论填充级别如何，结果始终占 cells+2 个单元格。
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

// contextStyle 把上下文用量百分比映射到现有调色板角色：
// 一半以下为 muted，到 85 为 warning，再高为 failure。被抑制的颜色
// （--no-color 或降级的终端）保持纯文本，因此没有 SGR 转义泄漏
// 到该行其余部分已无样式的头部中。
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
		// 旧的直接写入者仍然渲染，但新通知总是使用上面的
		// 栈，且两者同时存在时优先。
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
	// 主题只有在与默认不同时才值得永久提醒；深色不需要标签，因为
	// 它是历史外观。
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

// paintApproval 在清空的条带上绘制弹窗，使背后的记录
// 绝不会从框的边距透出。
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

// approvalLines 绘制阻塞输入直到按下 y / n / a 的弹窗。
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
	// 危险调用显示其被标记的原因，并把 Deny 标记为
	// 预选默认值。普通请求完全跳过此块，因此上面的弹窗和下面的提示行
	// 对它们逐字节不变。
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

// entryCacheKey 捕获所有可能改变某个条目渲染方式的输入。
// 把它集中在这里，可在向 Entry 添加新字段时保持缓存诚实：
// 忘记把它加入键只需在一处改一行。
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

// bodyLinesLocked 渲染记录，对键未变的每个条目复用缓存行。
// 因此一个流式增量只重绘一条
// 块，且主题变更通过 themeEpoch 使它们全部失效。
func (m *Model) bodyLinesLocked(width int) []Line {
	m.flushStreamLocked()
	if len(m.entryCache) > len(m.entries) {
		m.entryCache = m.entryCache[:len(m.entries)]
	}
	out := make([]Line, 0, len(m.entries)*2)
	for i, entry := range m.entries {
		var lines []Line
		if entry.Kind == EntryReasoning || isLiveToolEntry(entry) {
			// 摘要/spinner 带有实时经过时间，它会在
			// 没有任何条目字段变化的情况下改变，因此绝不能从缓存读回。
			// 但仍在索引 i 处存储一个槽，以保持缓存对齐。
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

// renderEntryLocked 精确绘制一个记录块，包括其上的
// 尝试徽章。
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

// attemptLinesLocked 在所属行的上方绘制未提交结果徽章。
// 措辞已本地化；形状保持稳定以便测试。
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

// reasoningSummaryMaxCells 限制提取的短语，使单个思考行
// 保持紧凑；reasoningMinPhraseCells 丢弃过短的首句
// （"Ok."），改用后面更有信息量的文本。
const (
	reasoningSummaryMaxCells = 48
	reasoningMinPhraseCells  = 8
)

// thinkingLabel 把 i18n 查找放在 reasoningSummaryLine 之外，后者的第一个
// 参数有意遮蔽了包级 text() 辅助函数。
func thinkingLabel(locale string) string { return text(locale, "thinking") }

// reasoningPhrase 把一个推理块扁平化为一个简短的关键短语：第一个
// 实质性的句子（或在没有合格句子时取整个扁平化文本），
// 用省略号截断到 max 个单元格。Truncate/Clip 感知字素，因此
// CJK 文本和 emoji 绝不会在 rune 中间被切开。
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

// reasoningSummaryLine 把任意量的思考文本渲染为恰好一行：
// 活动行显示 spinner 和省略号，已完成行显示经过的
// 时长。先测量固定装饰，使短语得到剩余的
// 单元格，并把整行裁剪到宽度。签名保证
// 只返回一个 Line，因此很长的推理绝不会重排记录布局。
func reasoningSummaryLine(text string, elapsed time.Duration, width int, done bool, locale string, p Palette) Line {
	line := NewLine()

	spinner := ""
	if !done {
		// 从 elapsed 推导帧使此函数保持纯（无 tick
		// 参数），同时仍能随调用方时钟推进而动画化。
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
	// Entry 没有公开的计时字段，因此在首次绘制时打上私有的展示时间戳。
	// 原始推理文本从不被修改或丢弃。
	idx := -1
	for i := len(m.entries) - 1; i >= 0; i-- {
		candidate := &m.entries[i]
		if candidate.Kind == EntryReasoning && candidate.Text == entry.Text {
			idx = i
			break
		}
	}
	// 尾随的推理块仅在回合仍在运行时是活动的；
	// 一旦 agent 继续（或回合结束），它显示最终时长。
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
	if ReasoningTailWindow {
		return m.reasoningWindowedLines(entry, width, elapsed, !live)
	}
	return []Line{reasoningSummaryLine(entry.Text, elapsed, width, !live, m.locale, m.palette)}
}

// --- 流式工具参数 ------------------------------------------------
//
// 一次工具调用可能在其参数 JSON 仍在流入时到达。
// 这些辅助函数恢复已经完整的顶层字段，使
// 待处理行能显示有用的预览，而不是半成品的 JSON 块。
// 它们是纯函数且从不报错：格式错误或不完整的输入只会
// 产生更少（或没有）字段。

// argField 是从流式参数 JSON 对象中恢复的一个顶层字段。
type argField struct {
	Key   string
	Value string
}

// toolElapsedThreshold 是这样一个临界点：运行中的工具自身的经过时间
// 在待处理行中变得比整回合时钟更有信息量。在此之前，
// 该行显示回合经过时间；超过之后，该行切换到工具自身的
// 经过时间，使单个缓慢的调用凸显出来。
const toolElapsedThreshold = 2 * time.Second

// partialArgsSummary 提取可能仍在流入的工具参数 JSON 对象中
// 已完成的顶层字段。没有闭合引号的最终字符串用
// 目前累积的文本挽救；不完整的尾部转义或 \uXXXX 被丢弃，
// 而不是泄漏替换 rune。
// 非对象、格式错误和空输入不产生字段。
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

// scanJSONString 解码从起始引号开始的 JSON 字符串。当字符串
// 被截断（尚无闭合引号）时，它返回带 complete=false 的
// 已解码前缀；不完整的尾部转义或 \u 转义被丢弃。
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
			// 未知转义：保留字面字节（宽容处理，绝不报错）。
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String(), i, false // EOF before the closing quote
}

// parseHex4 读取索引 u 处 'u' 之后的四个十六进制数字。
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

// scanUnicodeEscape 解码索引 u（指向 'u'）处的 \uXXXX，把
// 高/低代理对合并为一个 rune。不完整的代码点被丢弃。
func scanUnicodeEscape(s string, u int) (rune, int, bool) {
	code, next, ok := parseHex4(s, u)
	if !ok {
		return 0, next, false
	}
	if code >= 0xDC00 && code <= 0xDFFF {
		return 0, next, false // lone low surrogate: drop
	}
	if code >= 0xD800 && code <= 0xDBFF {
		// 高代理需要一个随后的 \uXXXX 低代理；当那一半
		// 仍在流入时，丢弃该对而不是显示乱码。
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

// scanJSONValue 返回 i 处值的简短摘要及其后紧接的索引。
// 字符串解码转义；对象/数组折叠为 "{…}"/"[…]"；
// 标量保留其字面文本。所有路径都推进 i，使调用方
// 不会在截断输入上无限循环。
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

// scanJSONContainer 扫描平衡的 {...} 或 [...]（感知字符串），
// 并返回紧凑的占位符。被截断的容器仍被摘要，使
// 该字段不会丢失；调用方的字段循环在 EOF 处停止。
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

// formatPartialArgs 把恢复的字段渲染为紧凑的 "key=value" 列表，
// 上限为三个字段和 48 个单元格。空输入产生空字符串。
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

// adaptiveElapsed 选择待处理行显示的经过时间：整回合
// 时钟，直到工具自身的经过时间超过阈值，然后显示工具时间。
func adaptiveElapsed(tool, turn, threshold time.Duration) time.Duration {
	if threshold <= 0 || tool >= threshold {
		return tool
	}
	return turn
}

// isLiveToolEntry 报告某个条目是否仍需要活动（未缓存）的行，
// 因为其 spinner/经过时间会在没有任何条目字段变化时改变。
func isLiveToolEntry(entry Entry) bool {
	return entry.Kind == EntryTool && (entry.Running || entry.Attempt == AttemptCancelled)
}

// liveToolLines 用流式参数预览渲染运行中或已取消的工具条目。
// 它读取匹配的活动任务（不修改状态）以获取工具特定的经过时间，
// 并使用回合时钟作为整轮经过时间。
// 一个条目始终恰好产生一行。
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

// toolPendingLine 渲染一行工具调用。活动调用动画化 spinner 并
// 显示自适应经过时间；已完成或失败的调用显示其结果字形和
// 最终时长；已取消的调用显示取消字形和标签。
// 参数通过 formatPartialArgs 从可能仍在流入的 JSON 渲染。
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

// hintLine 是底部按键栏。
func hintLine(width int, locale string, modal bool, style Style) Line {
	hint := text(locale, "hint")
	if modal {
		hint = text(locale, "hint_modal")
	}
	line := NewLine()
	line.Push(ShortHelp(HintBindings(hint), width, TextWidth), style)
	return line
}
