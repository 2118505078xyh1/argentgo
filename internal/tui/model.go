package tui

import (
	"context"
	"strings"
	"sync"
	"time"
)

// EntryKind classifies a transcript block.
type EntryKind int

// Transcript block kinds.
const (
	EntryUser EntryKind = iota
	EntryAssistant
	EntryReasoning
	EntryTool
	EntryNotice
)

// AttemptKind is the non-committed outcome attached to a transcript block.
// It is display metadata only: the agent history never sees these labels.
type AttemptKind string

// Attempt outcomes the UI can badge.
const (
	AttemptFailed      AttemptKind = "failed"
	AttemptRetried     AttemptKind = "retried"
	AttemptCancelled   AttemptKind = "cancelled"
	AttemptStreamError AttemptKind = "stream_error"
)

// Entry is one transcript block. Assistant text accumulates while streaming;
// tool entries carry the call and a truncated output. Attempt is set for
// failed / retried / cancelled rows so the operator can see them at a glance.
type Entry struct {
	Kind    EntryKind
	Text    string
	Name    string
	Args    string
	Output  string
	Running bool
	Failed  bool
	Attempt AttemptKind

	live bool // the assistant entry still receiving stream deltas

	// Display-only timing for a reasoning block. Entry has no public time
	// field; these are stamped on first paint by reasoningLines and used only
	// to build the one-line summary, so no public API or event changes.
	thinkingAt  time.Time
	thinkingEnd time.Time
}

// SessionInfo is one row of the optional session rail. Title is the generated
// (or explicitly renamed) title; Path stays available for tooltips and hooks.
type SessionInfo struct {
	Path  string
	Title string
}

// BufferPos is a 0-based (row, col) position in the rendered buffer. Mouse
// reports are 1-based; the run loop subtracts one before calling the
// selection methods.
type BufferPos struct {
	Row int
	Col int
}

// TurnAction is the narrow seam for undo / retry / rename. The TUI never
// imports the session or app package; an integrator supplies the behaviour.
type TurnAction func(ctx context.Context, arg string) error

// Config seeds a Model.
type Config struct {
	Root     string
	Model    string
	Locale   string
	Theme    string
	NoColor  bool
	Env      func(string) string // injected lookup for NO_COLOR / TERM
	FS       FileSystem          // injected read-only workspace; nil uses os
	Sessions []string
}

// KeyAction tells the event loop what a key asks for after the model update.
type KeyAction int

// Actions a key can produce.
const (
	ActionNone KeyAction = iota
	ActionSubmit
	ActionAbort
	ActionQuit
	ActionUndo
	ActionRetry
	ActionRename
)

// entryCacheKey captures every input that changes how one transcript entry is
// rendered. Keeping it per entry (rather than one key for the whole body) is
// what lets a streamed delta or a tool status change repaint a single block.
type entryCacheKey struct {
	kind       EntryKind
	text       string
	name       string
	args       string
	output     string
	running    bool
	failed     bool
	attempt    AttemptKind
	width      int
	expanded   bool
	noColor    bool
	themeEpoch int
	locale     string
}

// entryCacheSlot is one rendered entry aligned by index with Model.entries.
type entryCacheSlot struct {
	key   entryCacheKey
	lines []Line
}

// Model is the whole UI state. It owns no terminal: every method can be driven
// from a test, and the run loop is the only thing that touches the screen.
type Model struct {
	mu sync.Mutex

	model    string
	root     string
	locale   string
	sessions []SessionInfo

	theme    string
	palette  Palette
	noColor  bool
	degraded bool
	env      func(string) string

	entries   []Entry
	editor    Editor
	stream    strings.Builder
	streamBuf MarkdownStreamBuf // holds back incomplete markdown markers

	scroll     int
	bodyH      int
	version    int
	entryCache []entryCacheSlot

	// Text selection in 0-based buffer coordinates. selecting is true from
	// MouseDown until MouseUp; anchor and active survive MouseUp so the range
	// can still be copied afterwards.
	selAnchor BufferPos
	selActive BufferPos
	selecting bool

	// dirty records content changes between paints. The run loop gates its
	// repaint on it; the buffer diff remains the final authority on what is
	// actually written to the terminal.
	dirty      bool
	themeEpoch int // bumped when palette/font-related state changes

	status   string
	statusAt time.Time
	started  time.Time

	working   bool
	quit      bool
	submitted string

	usageIn     int
	usageOut    int
	usageCached int
	expanded    bool

	// Optional header metadata. Both unset keeps the classic header exactly
	// as it was before these fields existed.
	contextWindow int
	cost          string

	// Action hooks are installed by the integrator. nil means the command is
	// recognised but answered with a clear "not wired" notice.
	undoFn    TurnAction
	retryFn   TurnAction
	renameFn  TurnAction
	actionArg string

	// The optional side rail. All three are hidden by default so the classic
	// transcript view and its keybindings are byte-for-byte unchanged.
	showTasks      bool
	showWorkspace  bool
	showPlan       bool
	showSessions   bool
	sessionsCursor int
	sessionsScroll int
	// sessionFilter is the parsed fuzzy query shown above the session rows;
	// sessionFiltering is true while that filter bar owns the keyboard.
	// Both are inert until the session rail is shown.
	sessionFilter    string
	sessionFiltering bool
	tasks            []Task
	tasksScroll      int
	workspace        []workspaceEntry
	workspaceCursor  int
	workspaceScroll  int
	previewLines     []string
	previewPath      string
	previewScroll    int
	plan             *Plan
	planScroll       int
	fs               FileSystem

	pending       *approvalRequest
	approvalQueue []approvalRequest
	allowed       map[string]bool
	approvalFn    func(context.Context, string, string) (bool, error)

	now func() time.Time
}

// New returns a model ready to render. An empty Theme selects the dark
// palette, keeping callers that predate themes unchanged.
func New(cfg Config) *Model {
	locale := cfg.Locale
	if locale == "" {
		locale = "en"
	}
	m := &Model{
		model:    cfg.Model,
		root:     cfg.Root,
		locale:   locale,
		sessions: sessionInfos(cfg.Sessions),
		editor:   NewEditor(),
		started:  time.Now(),
		now:      time.Now,
		allowed:  map[string]bool{},
		env:      cfg.Env,
		fs:       cfg.FS,
		noColor:  cfg.NoColor,
		degraded: detectDegradation(cfg.Env),
	}
	m.theme = ThemeDark
	m.palette, _ = paletteFor(ThemeDark)
	_ = m.applyThemeLocked(cfg.Theme)
	if m.fs == nil {
		m.fs = osFileSystem{}
	}
	if m.degraded && !m.noColor {
		m.appendNoticeLocked(text(locale, "color_degraded"))
	}
	return m
}

// SetContextInfo supplies the optional header metadata: the model's context
// window (for the usage percentage) and a preformatted cost string. Passing 0
// and "" restores the original header. Safe to call while the run loop paints.
func (m *Model) SetContextInfo(window int, cost string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if window < 0 {
		window = 0
	}
	m.contextWindow = window
	m.cost = cost
	m.markDirtyLocked()
}

// MouseDown starts a selection at a 0-based buffer position. It replaces any
// previous selection.
func (m *Model) MouseDown(col, row int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.selAnchor = BufferPos{Row: row, Col: col}
	m.selActive = m.selAnchor
	m.selecting = true
	m.markDirtyLocked()
}

// MouseDrag extends the current selection. A drag with no active MouseDown is
// ignored, so stray motion reports cannot create a selection.
func (m *Model) MouseDrag(col, row int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.selecting {
		return
	}
	m.selActive = BufferPos{Row: row, Col: col}
	m.markDirtyLocked()
}

// MouseUp ends the drag and keeps the range available for SelectedText.
func (m *Model) MouseUp(col, row int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.selecting {
		return
	}
	m.selActive = BufferPos{Row: row, Col: col}
	m.selecting = false
	m.markDirtyLocked()
}

// ClearSelection drops the current selection entirely.
func (m *Model) ClearSelection() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.selAnchor = BufferPos{}
	m.selActive = BufferPos{}
	m.selecting = false
	m.markDirtyLocked()
}

// SelectionActive reports whether a drag is currently in progress.
func (m *Model) SelectionActive() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.selecting
}

// HasSelection reports whether there is a non-empty range to copy.
func (m *Model) HasSelection() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.hasSelectionLocked()
}

func (m *Model) hasSelectionLocked() bool { return m.selAnchor != m.selActive }

// selectionRangeLocked returns the selection endpoints in reading order
// (top-left first), regardless of drag direction.
func (m *Model) selectionRangeLocked() (BufferPos, BufferPos) {
	a, b := m.selAnchor, m.selActive
	if b.Row < a.Row || (b.Row == a.Row && b.Col < a.Col) {
		a, b = b, a
	}
	return a, b
}

// SelectedText extracts the plain text of the cells covered by the selection.
// Wide-rune continuation cells are skipped and each line is right-trimmed, so
// the result is paste-friendly. It returns "" when there is no selection or
// the buffer is empty.
func (m *Model) SelectedText(buf *Buffer) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if buf == nil || !m.hasSelectionLocked() || buf.Width() <= 0 || buf.Height() <= 0 {
		return ""
	}
	a, b := m.selectionRangeLocked()
	a.Row = clampCell(a.Row, 0, buf.Height()-1)
	b.Row = clampCell(b.Row, 0, buf.Height()-1)
	w := buf.Width()
	lines := make([]string, 0, b.Row-a.Row+1)
	for row := a.Row; row <= b.Row; row++ {
		start, end := 0, w-1
		if row == a.Row {
			start = a.Col
		}
		if row == b.Row {
			end = b.Col
		}
		if start > end {
			start, end = end, start
		}
		start, end = clampCell(start, 0, w-1), clampCell(end, 0, w-1)
		var line strings.Builder
		for col := start; col <= end; col++ {
			if r := buf.cells[row*w+col].R; r != 0 {
				line.WriteRune(r)
			}
		}
		lines = append(lines, strings.TrimRight(line.String(), " "))
	}
	return strings.Join(lines, "\n")
}

// MouseWheel maps a wheel report to the existing three-row scroll behaviour.
func (m *Model) MouseWheel(up bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if up {
		m.scrollByLocked(3)
	} else {
		m.scrollByLocked(-3)
	}
}

func clampCell(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
