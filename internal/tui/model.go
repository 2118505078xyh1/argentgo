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
}

// SessionInfo is one row of the optional session rail. Title is the generated
// (or explicitly renamed) title; Path stays available for tooltips and hooks.
type SessionInfo struct {
	Path  string
	Title string
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
	sessions []SessionInfo

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

	usageIn     int
	usageOut    int
	usageCached int
	expanded    bool

	// Action hooks are installed by the integrator. nil means the command is
	// recognised but answered with a clear "not wired" notice.
	undoFn    TurnAction
	retryFn   TurnAction
	renameFn  TurnAction
	actionArg string

	// The optional side rail. All three are hidden by default so the classic
	// transcript view and its keybindings are byte-for-byte unchanged.
	showTasks       bool
	showWorkspace   bool
	showPlan        bool
	showSessions    bool
	sessionsCursor  int
	sessionsScroll  int
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
	m.mu.Unlock()
}

// SetCachedTokens records the cached-prefix portion of the prompt tokens.
func (m *Model) SetCachedTokens(cached int) {
	m.mu.Lock()
	m.usageCached = cached
	m.mu.Unlock()
}

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
}

// AppendTool adds a finished or running tool call.
func (m *Model) AppendTool(name, args, output string, failed bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.flushStreamLocked()
	m.entries = append(m.entries, Entry{
		Kind:    EntryTool,
		Name:    name,
		Args:    args,
		Output:  output,
		Failed:  failed,
		Attempt: attemptFromFailed(failed),
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
	case KindCtrlS:
		m.showSessions = !m.showSessions
		m.version++
		if m.showSessions {
			m.setStatusLocked(text(m.locale, "sessions_on"))
		} else {
			m.setStatusLocked(text(m.locale, "sessions_off"))
		}
	case KindAltU:
		return m.requestActionLocked(ActionUndo, "")
	case KindAltR:
		return m.requestActionLocked(ActionRetry, "")
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
		if m.showSessions && len(m.sessions) > 0 {
			m.sessionsCursor = 0
			return ActionNone
		}
		m.editor.Home()
	case KindEnd, KindCtrlE:
		if m.showWorkspace && len(m.workspace) > 0 {
			m.moveWorkspaceCursorLocked(len(m.workspace))
			return ActionNone
		}
		if m.showSessions && len(m.sessions) > 0 {
			m.sessionsCursor = len(m.sessions) - 1
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
		if showSessions := m.showSessions && len(m.sessions) > 0; showSessions {
			if m.sessionsCursor > 0 {
				m.sessionsCursor--
			}
			m.clampSessionScrollLocked()
			return ActionNone
		}
		if m.showWorkspace {
			m.moveWorkspaceCursorLocked(-1)
			return ActionNone
		}
		if !m.editor.MoveLine(-1) {
			m.editor.HistoryPrev()
		}
	case KindDown:
		if showSessions := m.showSessions && len(m.sessions) > 0; showSessions {
			if m.sessionsCursor < len(m.sessions)-1 {
				m.sessionsCursor++
			}
			m.clampSessionScrollLocked()
			return ActionNone
		}
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
	arg := ""
	if i := strings.IndexAny(body, " \t"); i >= 0 {
		name = body[:i]
		arg = strings.TrimSpace(body[i+1:])
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
	case "/undo":
		return m.requestActionLocked(ActionUndo, arg)
	case "/retry":
		return m.requestActionLocked(ActionRetry, arg)
	case "/name":
		if arg == "" {
			m.setStatusLocked(text(m.locale, "name_usage"))
			return ActionNone
		}
		return m.requestActionLocked(ActionRename, arg)
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

// requestActionLocked reports an action to the run loop. An uninstalled hook
// keeps the command discoverable but never pretends it did anything.
func (m *Model) requestActionLocked(action KeyAction, arg string) KeyAction {
	if m.working {
		m.setStatusLocked(text(m.locale, "busy"))
		return ActionNone
	}
	hook := m.undoFn
	key := "undo_unwired"
	switch action {
	case ActionRetry:
		hook, key = m.retryFn, "retry_unwired"
	case ActionRename:
		hook, key = m.renameFn, "rename_unwired"
	case ActionUndo:
		key = "undo_unwired"
	}
	if hook == nil {
		m.appendNoticeLocked(text(m.locale, key))
		return ActionNone
	}
	m.actionArg = arg
	m.setStatusLocked("")
	return action
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
			if entry.Attempt != "" {
				out.WriteString("⟳ [" + string(entry.Attempt) + "] ")
			}
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
