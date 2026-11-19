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

	entries []Entry
	editor  Editor
	stream  strings.Builder

	scroll     int
	bodyH      int
	version    int
	entryCache []entryCacheSlot

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
