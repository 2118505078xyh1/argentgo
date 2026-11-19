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

// Entry is one transcript block. Assistant text accumulates while streaming;
// tool entries carry the call and a truncated output.
type Entry struct {
	Kind    EntryKind
	Text    string
	Name    string
	Args    string
	Output  string
	Running bool
	Failed  bool

	live bool // the assistant entry still receiving stream deltas
}

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
)

type bodyCache struct {
	width    int
	version  int
	expanded bool
	lines    []Line
}

// Model is the whole UI state. It owns no terminal: every method can be driven
// from a test, and the run loop is the only thing that touches the screen.
type Model struct {
	mu sync.Mutex

	model    string
	root     string
	locale   string
	sessions []string

	theme    string
	palette  Palette
	noColor  bool
	degraded bool
	env      func(string) string

	entries []Entry
	editor  Editor
	stream  strings.Builder

	scroll  int
	bodyH   int
	version int
	cache   *bodyCache

	status   string
	statusAt time.Time
	started  time.Time

	working   bool
	quit      bool
	submitted string

	usageIn  int
	usageOut int
	expanded bool

	// The optional side rail. All three are hidden by default so the classic
	// transcript view and its keybindings are byte-for-byte unchanged.
	showTasks       bool
	showWorkspace   bool
	showPlan        bool
	tasks           []Task
	tasksScroll     int
	workspace       []workspaceEntry
	workspaceCursor int
	workspaceScroll int
	previewLines    []string
	previewPath     string
	previewScroll   int
	plan            *Plan
	planScroll      int
	fs              FileSystem

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
		sessions: append([]string{}, cfg.Sessions...),
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
	return nil
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
	m.mu.Unlock()
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
}

func (m *Model) setStatusLocked(text string) {
	m.status = text
	m.statusAt = m.now()
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
	m.scroll = 0
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
}

// AppendTool adds a finished or running tool call.
func (m *Model) AppendTool(name, args, output string, failed bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.flushStreamLocked()
	m.entries = append(m.entries, Entry{
		Kind:   EntryTool,
		Name:   name,
		Args:   args,
		Output: output,
		Failed: failed,
	})
	m.version++
	m.scroll = 0
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
}

// HandleKey applies one keystroke and reports what the loop should do.
func (m *Model) HandleKey(key Key) KeyAction {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.handleKeyLocked(key)
}

func (m *Model) handleKeyLocked(key Key) KeyAction {
	if m.pending != nil {
		return m.answerApprovalLocked(key)
	}
	switch key.Kind {
	case KindIgnore:
		return ActionNone
	case KindCtrlT:
		m.showTasks = !m.showTasks
		m.version++
		if m.showTasks {
			m.setStatusLocked(text(m.locale, "tasks_on"))
		} else {
			m.setStatusLocked(text(m.locale, "tasks_off"))
		}
	case KindCtrlB:
		m.showWorkspace = !m.showWorkspace
		if m.showWorkspace {
			m.buildWorkspaceLocked()
		}
		m.version++
		if m.showWorkspace {
			m.setStatusLocked(text(m.locale, "workspace_on"))
		} else {
			m.setStatusLocked(text(m.locale, "workspace_off"))
		}
	case KindCtrlP:
		m.showPlan = !m.showPlan
		m.version++
		if m.showPlan {
			m.setStatusLocked(text(m.locale, "plan_on"))
		} else {
			m.setStatusLocked(text(m.locale, "plan_off"))
		}
	case KindCtrlC:
		if m.working {
			m.setStatusLocked(text(m.locale, "cancelling"))
			return ActionAbort
		}
		if strings.TrimSpace(m.editor.Text()) != "" {
			m.editor.Clear()
			return ActionNone
		}
		m.quit = true
		return ActionQuit
	case KindEsc:
		if m.working {
			m.setStatusLocked(text(m.locale, "cancelling"))
			return ActionAbort
		}
		m.scroll = 0
		return ActionNone
	case KindEnter:
		return m.submitLocked()
	case KindAltEnter, KindNewline:
		m.editor.Insert("\n")
		return ActionNone
	case KindBackspace:
		m.editor.Backspace()
	case KindDelete:
		m.editor.Delete()
	case KindLeft:
		m.editor.Left()
	case KindRight:
		m.editor.Right()
	case KindHome, KindCtrlA:
		if m.showWorkspace && len(m.workspace) > 0 {
			m.moveWorkspaceCursorLocked(-m.workspaceCursor)
			return ActionNone
		}
		m.editor.Home()
	case KindEnd, KindCtrlE:
		if m.showWorkspace && len(m.workspace) > 0 {
			m.moveWorkspaceCursorLocked(len(m.workspace))
			return ActionNone
		}
		m.editor.End()
	case KindCtrlK:
		m.editor.KillToEnd()
	case KindCtrlU:
		m.editor.KillToStart()
	case KindCtrlW:
		m.editor.KillWord()
	case KindUp:
		if m.showWorkspace {
			m.moveWorkspaceCursorLocked(-1)
			return ActionNone
		}
		if !m.editor.MoveLine(-1) {
			m.editor.HistoryPrev()
		}
	case KindDown:
		if m.showWorkspace {
			m.moveWorkspaceCursorLocked(1)
			return ActionNone
		}
		if !m.editor.MoveLine(1) {
			m.editor.HistoryNext()
		}
	case KindPageUp:
		m.scrollByLocked(maxInt(m.bodyH, 10))
	case KindPageDown:
		m.scrollByLocked(-maxInt(m.bodyH, 10))
	case KindScrollUp:
		m.scrollByLocked(3)
	case KindScrollDown:
		m.scrollByLocked(-3)
	case KindTab:
		m.editor.Insert("  ")
	case KindRune:
		m.editor.Insert(string(key.Rune))
	case KindPaste:
		m.editor.Insert(key.Text)
	}
	return ActionNone
}

func (m *Model) submitLocked() KeyAction {
	raw := m.editor.Text()
	body := strings.TrimRight(raw, "\n")
	if strings.TrimSpace(body) == "" {
		return ActionNone
	}
	if m.working {
		m.setStatusLocked(text(m.locale, "busy"))
		return ActionNone
	}
	m.editor.Record(body)
	m.editor.Clear()
	if strings.HasPrefix(body, "/") {
		return m.commandLocked(body)
	}
	m.flushStreamLocked()
	if n := len(m.entries); n > 0 {
		m.entries[n-1].live = false
	}
	m.appendUserLocked(body)
	m.working = true
	m.scroll = 0
	m.submitted = body
	m.setStatusLocked("")
	return ActionSubmit
}

func (m *Model) commandLocked(body string) KeyAction {
	name := body
	if i := strings.IndexAny(body, " \t"); i >= 0 {
		name = body[:i]
	}
	switch name {
	case "/quit", "/exit":
		m.quit = true
		return ActionQuit
	case "/clear":
		m.entries = nil
		m.scroll = 0
		m.version++
	case "/expand":
		m.expanded = !m.expanded
		m.version++
		if m.expanded {
			m.setStatusLocked(text(m.locale, "expanded"))
		} else {
			m.setStatusLocked(text(m.locale, "collapsed"))
		}
	case "/help":
		m.appendNoticeLocked(text(m.locale, "help"))
	default:
		m.appendUserLocked(body)
		m.working = true
		m.scroll = 0
		m.submitted = body
		return ActionSubmit
	}
	return ActionNone
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
				out.WriteString(entry.Output + "\n")
			}
		case EntryNotice:
			out.WriteString("! " + entry.Text + "\n")
		}
	}
	return out.String()
}

func errorText(locale string, err error) string {
	if err == context.Canceled {
		return text(locale, "cancelled")
	}
	return text(locale, "error") + ": " + err.Error()
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
