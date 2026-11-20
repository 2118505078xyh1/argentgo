package tui

import (
	"context"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// MarkDirty records that the rendered content changed and the next frame must
// be painted. It is safe to call from any goroutine.
func (m *Model) MarkDirty() {
	m.mu.Lock()
	m.markDirtyLocked()
	m.mu.Unlock()
}

func (m *Model) markDirtyLocked() {
	m.dirty = true
}

// Dirty reports whether content changed since the last paint.
func (m *Model) Dirty() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.dirty
}

// ClearDirty acknowledges a paint. Most callers never need it: Paint clears
// the flag itself.
func (m *Model) ClearDirty() {
	m.mu.Lock()
	m.dirty = false
	m.mu.Unlock()
}

// SetTheme switches the palette at runtime. Unknown names leave the current
// theme in place and return an error wrapping ErrUnknownTheme.
func (m *Model) SetTheme(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.applyThemeLocked(name)
}

// Theme is the canonical name of the active theme.
func (m *Model) Theme() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.theme
}

// Palette returns the palette currently driving the renderer.
func (m *Model) Palette() Palette {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.palette
}

// SetNoColor toggles colour suppression without rebuilding the model.
func (m *Model) SetNoColor(v bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.noColor == v {
		return
	}
	m.noColor = v
	_ = m.applyThemeLocked(m.theme)
}

// NoColor reports whether colour SGR codes are suppressed.
func (m *Model) NoColor() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.noColor
}

// Degraded reports whether the injected environment asked for monochrome.
func (m *Model) Degraded() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.degraded
}

// applyThemeLocked rebuilds the palette from the theme, environment and
// NoColor flag. It always bumps the version so a repaint picks up the change.
func (m *Model) applyThemeLocked(name string) error {
	p, err := paletteFor(name)
	if err != nil {
		return err
	}
	if m.degraded || m.noColor {
		p = monochrome(p)
	}
	p.noColor = m.noColor
	m.theme = canonicalTheme(name)
	m.palette = p
	m.version++
	m.themeEpoch++
	m.markDirtyLocked()
	return nil
}

func sessionInfos(paths []string) []SessionInfo {
	out := make([]SessionInfo, 0, len(paths))
	for _, p := range paths {
		title := p
		if i := strings.LastIndexAny(p, `/\\`); i >= 0 {
			title = p[i+1:]
		}
		title = strings.TrimSuffix(title, ".jsonl")
		out = append(out, SessionInfo{Path: p, Title: title})
	}
	return out
}

// SetSessions replaces the session rail. An empty slice hides the rail but
// keeps the previously selected row for the next fill.
func (m *Model) SetSessions(rows []SessionInfo) {
	m.mu.Lock()
	m.sessions = append([]SessionInfo{}, rows...)
	m.sessionsCursor = clampScroll(m.sessionsCursor, maxInt(len(m.sessions)-1, 0))
	m.version++
	m.markDirtyLocked()
	m.mu.Unlock()
}

// Sessions returns a copy of the session rail rows.
func (m *Model) Sessions() []SessionInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]SessionInfo{}, m.sessions...)
}

// SetTurnActions installs the undo / retry / rename seams. Passing nil clears
// one action, which makes its slash command answer with a notice instead.
func (m *Model) SetTurnActions(undo, retry, rename TurnAction) {
	m.mu.Lock()
	m.undoFn, m.retryFn, m.renameFn = undo, retry, rename
	m.mu.Unlock()
}

// TakeActionArg returns the argument of the last action-producing command.
func (m *Model) TakeActionArg() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	arg := m.actionArg
	m.actionArg = ""
	return arg
}

// SetApproval installs the injected approval authority. It is consulted after
// the operator presses y/a; a nil function means the answer is final.
func (m *Model) SetApproval(fn func(ctx context.Context, name, args string) (bool, error)) {
	m.mu.Lock()
	m.approvalFn = fn
	m.mu.Unlock()
}

// Quit reports whether the operator asked to leave.
func (m *Model) Quit() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.quit
}

// Busy reports whether a turn is running.
func (m *Model) Busy() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.working
}

// Input is the current prompt text.
func (m *Model) Input() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.editor.Text()
}

// Cursor is the caret position as a rune index.
func (m *Model) Cursor() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.editor.Cursor()
}

// Transcript is a copy of the current blocks.
func (m *Model) Transcript() []Entry {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.flushStreamLocked()
	return append([]Entry{}, m.entries...)
}

// TakeSubmitted returns the text of the last submitted prompt and clears it.
func (m *Model) TakeSubmitted() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	text := m.submitted
	m.submitted = ""
	return text
}

// SetUsage records token totals for the header.
func (m *Model) SetUsage(prompt, completion int) {
	m.mu.Lock()
	m.usageIn, m.usageOut = prompt, completion
	m.markDirtyLocked()
	m.mu.Unlock()
}

// SetCachedTokens records the cached-prefix portion of the prompt tokens.
func (m *Model) SetCachedTokens(cached int) {
	m.mu.Lock()
	m.usageCached = cached
	m.markDirtyLocked()
	m.mu.Unlock()
}

// Plain renders the transcript as unstyled text, for the scroll-back that
// stays in the terminal after the alternate screen is left.
func (m *Model) Plain() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.flushStreamLocked()
	var out strings.Builder
	for _, entry := range m.entries {
		switch entry.Kind {
		case EntryUser:
			out.WriteString("> " + entry.Text + "\n")
		case EntryAssistant:
			out.WriteString("◆ " + entry.Text + "\n")
		case EntryReasoning:
			out.WriteString("◇ " + entry.Text + "\n")
		case EntryTool:
			out.WriteString("● " + entry.Name + "(" + summarizeArgs(entry.Args) + ")\n")
			if entry.Output != "" {
				out.WriteString(plainOutputPreview(entry.Output))
			}
		case EntryNotice:
			if entry.Attempt != "" {
				out.WriteString("⟳ [" + string(entry.Attempt) + "] ")
			}
			out.WriteString("! " + entry.Text + "\n")
		}
	}
	return out.String()
}

// plainOutputPreview keeps the scroll-back short. The full output already
// lives in Entry.Output for the interactive view, but dumping it on exit would
// flood the terminal; only a bounded head is handed back here.
func plainOutputPreview(output string) string {
	const (
		maxPreviewBytes = 512
		maxPreviewLines = 3
	)
	var out strings.Builder
	used, lines, truncated := 0, 0, false
	i := 0
	for i < len(output) && lines < maxPreviewLines && used < maxPreviewBytes {
		end := len(output)
		if nl := strings.IndexByte(output[i:], '\n'); nl >= 0 {
			end = i + nl + 1
		}
		segment := output[i:end]
		if len(segment) > maxPreviewBytes-used {
			out.WriteString(segment[:runeFloor(segment, maxPreviewBytes-used)])
			truncated = true
			break
		}
		out.WriteString(segment)
		used += len(segment)
		lines++
		i = end
	}
	if i < len(output) {
		truncated = true
	}
	if truncated {
		out.WriteString("… (output omitted)\n")
	}
	return out.String()
}

func errorText(locale string, err error) string {
	if err == context.Canceled {
		return text(locale, "cancelled")
	}
	// A turn-ending error may still carry a raw provider message (the agent also
	// publishes a redacted EventError). Redact, fold to one line and bound it so
	// a credential or a multi-line dump cannot leak into or widen the transcript.
	detail := Truncate(collapseSpace(llm.RedactText(err.Error())), maxStreamErrorDetail)
	return text(locale, "error") + ": " + detail
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// visibleWindow returns the half-open item interval [start, end) that a
// scrolled list should render: the viewport starting at scrollOffset and
// spanning viewportHeight items, grown by overscan items on each side so rows
// just outside the view are warm when the operator scrolls.
//
// It is a pure integer helper (it counts items, not bytes or runes, so CJK and
// wrapped display widths are irrelevant here) and is safe for out-of-range
// input: negative totals yield an empty window, offsets are clamped into
// [0, total], a non-positive viewport yields an empty window at the offset,
// and both ends always stay within [0, total] with start <= end.
//
// This is the reusable basis for windowing the transcript in
// bodyLinesLocked; wiring it in is deliberately left to the owner of
// render.go.
func visibleWindow(total, scrollOffset, viewportHeight, overscan int) (start, end int) {
	if total <= 0 {
		return 0, 0
	}
	if scrollOffset < 0 {
		scrollOffset = 0
	} else if scrollOffset > total {
		scrollOffset = total
	}
	if viewportHeight < 0 {
		viewportHeight = 0
	}
	if overscan < 0 {
		overscan = 0
	}
	// A value larger than the list cannot widen a clamped window, so capping
	// here keeps the additions below from overflowing on extreme inputs.
	if viewportHeight > total {
		viewportHeight = total
	}
	if overscan > total {
		overscan = total
	}
	if viewportHeight == 0 {
		return scrollOffset, scrollOffset
	}
	start = scrollOffset - overscan
	if start < 0 {
		start = 0
	}
	end = scrollOffset + viewportHeight + overscan
	if end > total {
		end = total
	}
	if end < start {
		end = start
	}
	return start, end
}
