package tui

import (
	"os"
	"os/exec"
	"strings"
)

// externalEditorRun spawns the resolved editor on a temporary prompt file. It
// is a package variable, like clipboardWrite and editorLookPath, so tests can
// swap in a fake editor without launching a real process. Production wires the
// editor to the real terminal streams because it owns the screen while running.
var externalEditorRun = func(cmd string, args []string, path string) error {
	all := append(append([]string{}, args...), path)
	c := exec.Command(cmd, all...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stdout
	return c.Run()
}

// externalEditorScreen, when installed by the run loop, wraps the editor
// process so the TUI can leave the alternate screen (and raw mode) first. A nil
// callback keeps model-only callers, including tests, working unchanged.
var externalEditorScreen func(fn func())

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
	m.markDirtyLocked()
	if m.sessionFiltering {
		return m.filterKeyLocked(key)
	}
	switch key.Kind {
	case KindIgnore:
		return ActionNone
	case KindCtrlL:
		// Ctrl+L is decoded but otherwise unused; with the session rail open
		// it focuses the filter bar. It leaves the prompt untouched so slash
		// commands like /help still type normally.
		if m.showSessions {
			m.sessionFiltering = true
		}
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
	case KindCtrlG:
		return m.openExternalEditorLocked()
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

// openExternalEditorLocked edits the current prompt through $VISUAL / $EDITOR.
// The prompt is written to a temporary file, the editor is given exclusive use
// of the terminal, and the file is read back into the prompt. Every failure
// path is a no-op: the original text stays in place and the loop never breaks.
func (m *Model) openExternalEditorLocked() KeyAction {
	if m.working {
		m.setStatusLocked(text(m.locale, "busy"))
		return ActionNone
	}
	lookup := func(key string) (string, bool) {
		if m.env == nil {
			return "", false
		}
		value := m.env(key)
		return value, value != ""
	}
	cmd, args, ok := ResolveExternalEditor(lookup)
	if !ok {
		return ActionNone
	}

	file, err := os.CreateTemp("", "argent-prompt-*.md")
	if err != nil {
		return ActionNone
	}
	path := file.Name()
	defer os.Remove(path)
	original := m.editor.Text()
	if _, err := file.WriteString(original); err != nil {
		file.Close()
		return ActionNone
	}
	if err := file.Close(); err != nil {
		return ActionNone
	}

	wrap := externalEditorScreen
	if wrap == nil {
		wrap = func(fn func()) { fn() }
	}
	var runErr error
	wrap(func() { runErr = externalEditorRun(cmd, args, path) })
	if runErr != nil {
		return ActionNone
	}

	edited, err := os.ReadFile(path)
	if err != nil {
		return ActionNone
	}
	m.editor.SetText(string(edited))
	m.markDirtyLocked()
	return ActionNone
}

// filterKeyLocked routes keys while the session filter bar owns the keyboard.
// Filtering is live: every keystroke re-renders the rail. Esc cancels and
// clears the query, Enter confirms and keeps it, Ctrl+L leaves focus without
// changing the query. No key is ever forwarded to the prompt in this mode.
func (m *Model) filterKeyLocked(key Key) KeyAction {
	switch key.Kind {
	case KindEsc:
		m.sessionFiltering = false
		m.sessionFilter = ""
		m.sessionsScroll = 0
	case KindEnter:
		m.sessionFiltering = false
		m.sessionsScroll = 0
	case KindCtrlL:
		m.sessionFiltering = false
	case KindBackspace:
		runes := []rune(m.sessionFilter)
		if len(runes) > 0 {
			m.sessionFilter = string(runes[:len(runes)-1])
		}
		m.sessionsScroll = 0
	case KindRune:
		m.sessionFilter += string(key.Rune)
		m.sessionsScroll = 0
	case KindPaste:
		m.sessionFilter += key.Text
		m.sessionsScroll = 0
	case KindIgnore:
		// nothing to do
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
		m.entryCache = nil
		m.scroll = 0
		m.version++
		m.markDirtyLocked()
	case "/expand":
		m.expanded = !m.expanded
		m.version++
		m.markDirtyLocked()
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
