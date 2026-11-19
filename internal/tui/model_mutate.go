package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Tool output is bounded at the point it enters the model: a single read of a
// generated file must not pin 100 MB in memory until the process exits. The
// head+tail windows keep both the interesting start and the final lines (where
// errors usually are). The truncation is byte- and line-aware and never cuts a
// UTF-8 rune in half.
const (
	maxToolOutputBytes = 64 << 10
	maxToolOutputLines = 2000

	// markerReserve leaves room for the "[…N 字节省略…]" marker so the stored
	// string stays under maxToolOutputBytes even after the hint is appended.
	markerReserve = 64
)

// AppendAttempt adds a non-committed attempt badge to the transcript.
func (m *Model) AppendAttempt(kind AttemptKind, detail string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.appendAttemptLocked(kind, detail)
}

func (m *Model) appendAttemptLocked(kind AttemptKind, detail string) {
	m.flushStreamLocked()
	m.entries = append(m.entries, Entry{Kind: EntryNotice, Attempt: kind, Text: detail})
	m.version++
	m.markDirtyLocked()
}

// SetStatus shows a transient message on the status line.
func (m *Model) SetStatus(text string) {
	m.mu.Lock()
	m.setStatusLocked(text)
	m.mu.Unlock()
}

// Scroll moves the transcript back (positive) or toward the bottom (negative).
func (m *Model) Scroll(delta int) {
	m.mu.Lock()
	m.scrollByLocked(delta)
	m.mu.Unlock()
}

func (m *Model) scrollByLocked(delta int) {
	m.markDirtyLocked()
	m.scroll += delta
	if m.scroll < 0 {
		m.scroll = 0
	}
	if m.showTasks {
		m.tasksScroll = clampScroll(m.tasksScroll+delta, len(m.tasks))
	}
	if m.showPlan {
		max := 0
		if m.plan != nil {
			max = len(m.plan.Steps) + 1
		}
		m.planScroll = clampScroll(m.planScroll+delta, max)
	}
	if m.showWorkspace {
		m.workspaceScroll = clampScroll(m.workspaceScroll+delta, maxInt(len(m.workspace)-1, 0))
		m.previewScroll = clampScroll(m.previewScroll+delta, len(m.previewLines))
	}
	if m.showSessions {
		m.sessionsScroll = clampScroll(m.sessionsScroll+delta, maxInt(len(m.sessions)-1, 0))
	}
}

func (m *Model) clampSessionScrollLocked() {
	visible := maxInt(m.bodyH/4-1, 1)
	if m.sessionsCursor < m.sessionsScroll {
		m.sessionsScroll = m.sessionsCursor
	}
	if m.sessionsCursor >= m.sessionsScroll+visible {
		m.sessionsScroll = m.sessionsCursor - visible + 1
	}
}

func (m *Model) setStatusLocked(text string) {
	m.status = text
	m.statusAt = m.now()
	m.markDirtyLocked()
}

// AppendUser adds a user block.
func (m *Model) AppendUser(text string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.appendUserLocked(text)
}

func (m *Model) appendUserLocked(text string) {
	m.flushStreamLocked()
	m.entries = append(m.entries, Entry{Kind: EntryUser, Text: text})
	m.version++
	m.markDirtyLocked()
	m.scroll = 0
}

func attemptFromFailed(failed bool) AttemptKind {
	if failed {
		return AttemptFailed
	}
	return ""
}

// AppendReasoning adds (or extends) a reasoning block.
func (m *Model) AppendReasoning(text string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.flushStreamLocked()
	if n := len(m.entries); n > 0 && m.entries[n-1].Kind == EntryReasoning {
		m.entries[n-1].Text += text
	} else {
		m.entries = append(m.entries, Entry{Kind: EntryReasoning, Text: text})
	}
	m.version++
	m.markDirtyLocked()
}

// AppendTool adds a finished or running tool call. Output is bounded here, at
// the ingress, so a single entry can never pin an unbounded string in memory.
func (m *Model) AppendTool(name, args, output string, failed bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.flushStreamLocked()
	m.entries = append(m.entries, Entry{
		Kind:    EntryTool,
		Name:    name,
		Args:    args,
		Output:  truncateToolOutput(output),
		Failed:  failed,
		Attempt: attemptFromFailed(failed),
	})
	m.version++
	m.markDirtyLocked()
	m.scroll = 0
}

// truncateToolOutput keeps a head and a tail window of output, dropping the
// middle. Both byte and line budgets are enforced, and every cut lands on a
// UTF-8 rune boundary so CJK text and emoji are never corrupted.
func truncateToolOutput(output string) string {
	if len(output) <= maxToolOutputBytes && strings.Count(output, "\n") < maxToolOutputLines {
		return output
	}
	budget := maxToolOutputBytes - markerReserve
	headBytes, tailBytes := budget/2, budget-budget/2
	headLines, tailLines := maxToolOutputLines/2, maxToolOutputLines-maxToolOutputLines/2

	// Head window: at most headBytes bytes and headLines lines.
	headEnd := runeFloor(output, headBytes)
	if cut := prefixLineEnd(output[:headEnd], headLines); cut < headEnd {
		headEnd = cut
	}
	// Tail window: at most tailBytes bytes and tailLines lines.
	tailStart := runeCeil(output, len(output)-tailBytes)
	if cut := suffixLineStart(output[tailStart:], tailLines); cut > 0 {
		tailStart += cut
	}
	if headEnd > tailStart {
		mid := runeFloor(output, len(output)/2)
		headEnd, tailStart = mid, runeCeil(output, mid)
	}

	head, tail := output[:headEnd], output[tailStart:]
	omitted := len(output) - len(head) - len(tail)
	marker := fmt.Sprintf("[…%d 字节省略…]", omitted)
	return head + marker + tail
}

// runeFloor returns the greatest i <= limit that starts a UTF-8 rune.
func runeFloor(s string, limit int) int {
	if limit >= len(s) {
		return len(s)
	}
	if limit <= 0 {
		return 0
	}
	i := limit
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return i
}

// runeCeil returns the smallest i >= limit that starts a UTF-8 rune.
func runeCeil(s string, limit int) int {
	if limit <= 0 {
		return 0
	}
	if limit >= len(s) {
		return len(s)
	}
	i := limit
	for i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return i
}

// prefixLineEnd returns the byte index just after the nth newline, or len(s).
func prefixLineEnd(s string, n int) int {
	count := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			count++
			if count == n {
				return i + 1
			}
		}
	}
	return len(s)
}

// suffixLineStart returns an offset into s such that the suffix covers at
// most n lines. A newline is ASCII, so the result is always a rune boundary.
func suffixLineStart(s string, n int) int {
	if n <= 0 {
		return len(s)
	}
	count := 0
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '\n' {
			count++
			if count == n {
				return i + 1
			}
		}
	}
	return 0
}

// AppendNotice adds a system notice.
func (m *Model) AppendNotice(text string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.appendNoticeLocked(text)
}

func (m *Model) appendNoticeLocked(text string) {
	m.flushStreamLocked()
	m.entries = append(m.entries, Entry{Kind: EntryNotice, Text: text})
	m.version++
	m.markDirtyLocked()
}

// AppendStream appends assistant text as it streams in. It does not bump the
// render version: the text is folded into the entry once per frame, so a turn
// of a thousand deltas costs one copy per repaint rather than one per delta.
func (m *Model) AppendStream(delta string) {
	if delta == "" {
		return
	}
	m.mu.Lock()
	m.stream.WriteString(delta)
	m.markDirtyLocked()
	m.mu.Unlock()
}

func (m *Model) flushStreamLocked() {
	if m.stream.Len() == 0 {
		return
	}
	text := m.stream.String()
	m.stream.Reset()
	if n := len(m.entries); n > 0 && m.entries[n-1].live {
		m.entries[n-1].Text += text
	} else {
		m.entries = append(m.entries, Entry{Kind: EntryAssistant, Text: text, live: true})
	}
	m.version++
	m.markDirtyLocked()
}

// EndTurn closes the running turn. A non-nil error becomes a notice.
func (m *Model) EndTurn(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.flushStreamLocked()
	if n := len(m.entries); n > 0 {
		m.entries[n-1].live = false
	}
	m.working = false
	if err != nil {
		m.appendNoticeLocked(errorText(m.locale, err))
	}
	m.version++
	m.markDirtyLocked()
}
