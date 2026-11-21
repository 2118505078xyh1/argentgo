package tui

import (
	"os"
	"os/exec"
	"strings"
	"unicode/utf8"
)

// externalEditorRun 在临时提示文件上启动解析出的编辑器。
// 它是包变量，像 clipboardWrite 和 editorLookPath 一样，使测试可以
// 换成假编辑器而不启动真实进程。生产环境把编辑器接到
// 真实的终端流，因为它在运行期间拥有屏幕。
var externalEditorRun = func(cmd string, args []string, path string) error {
	all := append(append([]string{}, args...), path)
	c := exec.Command(cmd, all...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stdout
	return c.Run()
}

// 当 run 循环安装 externalEditorScreen 时，它包装编辑器
// 进程，使 TUI 能先离开备用屏幕（和原始模式）。nil
// 回调让仅使用模型的调用方（包括测试）保持不变。
var externalEditorScreen func(fn func())

// HandleKey 应用一次按键并报告循环应做什么。
func (m *Model) HandleKey(key Key) KeyAction {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.handleKeyLocked(key)
}

func (m *Model) handleKeyLocked(key Key) KeyAction {
	if m.pending != nil {
		// 宽限期开启时，弹窗刚出现的这段时间内吞掉尾随按键，不把它当作
		// 审批回答。宽限期为 0 时完全跳过判断，沿用原来的直达路径。
		if grace := DialogKeyGracePeriod; grace > 0 && dialogGraceActive(m.now(), m.pending.openedAt, grace) {
			return ActionNone
		}
		return m.answerApprovalLocked(key)
	}
	m.markDirtyLocked()
	if m.sessionFiltering {
		return m.filterKeyLocked(key)
	}
	// vim 键位（可选）：默认关闭时 m.vimMode 为零值 false，守卫短路，主 switch
	// 逐字节不变。开启后由 handleVimKeyLocked 决定是否消费该键。
	if m.vimMode && m.handleVimKeyLocked(key) {
		return ActionNone
	}
	switch key.Kind {
	case KindIgnore:
		return ActionNone
	case KindCtrlL:
		// Ctrl+L 被解码但其他方面未使用；会话栏打开时
		// 它聚焦过滤栏。它不触碰提示，使 /help 这样的斜杠
		// 命令仍能正常输入。
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
	case KindCtrlO:
		m.showCommands = !m.showCommands
		m.version++
		if m.showCommands {
			m.setStatusLocked(text(m.locale, "commands_on"))
		} else {
			m.setStatusLocked(text(m.locale, "commands_off"))
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
		// 关闭当前补全与面板；下一次输入会按新文本重算 query 重新打开。
		m.completion = nil
		m.completionOverlay = false
		m.commandsCursor = 0
		m.commandsScroll = 0
		m.showCommands = false
		m.scroll = 0
		return ActionNone
	case KindEnter:
		// 候选可见时 Enter 接受候选项，而不是提交整行。
		if m.completionActiveLocked() {
			return m.acceptCompletionLocked()
		}
		return m.submitLocked()
	case KindAltEnter, KindNewline:
		m.editor.Insert("\n")
		return ActionNone
	case KindBackspace:
		m.editor.Backspace()
		m.refreshCompletionLocked()
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
		if m.completionActiveLocked() {
			m.completion.Prev()
			m.commandsCursor = clampScroll(m.completion.index, maxInt(m.completion.Count()-1, 0))
			m.clampCommandsScrollLocked()
			return ActionNone
		}
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
		if m.completionActiveLocked() {
			m.completion.Next()
			m.commandsCursor = clampScroll(m.completion.index, maxInt(m.completion.Count()-1, 0))
			m.clampCommandsScrollLocked()
			return ActionNone
		}
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
		// 仅当三条件同时成立时接管补全：斜杠开头、已注入补全状态、
		// 且过滤后仍有候选。否则严格保留旧行为——插入两个空格。
		if m.completionActiveLocked() {
			return m.acceptCompletionLocked()
		}
		m.editor.Insert("  ")
	case KindRune:
		m.editor.Insert(string(key.Rune))
		m.refreshCompletionLocked()
	case KindPaste:
		m.editor.Insert(key.Text)
		m.refreshCompletionLocked()
	}
	return ActionNone
}

// completionActiveLocked 报告输入行是否应由斜杠补全接管。
// 三条件缺一不可：文本以 '/' 开头、宿主注入过补全状态、过滤后仍有候选。
func (m *Model) completionActiveLocked() bool {
	if m.completion == nil || !strings.HasPrefix(m.editor.Text(), "/") {
		return false
	}
	return m.completion.Count() > 0
}

// refreshCompletionLocked 在文本变化后重算查询。Esc 关闭补全时把状态置空，
// 这里按需从 commandInfo 重建，使再次输入可以重新打开补全。
// 查询取 '/' 后的整段文本：一旦输入空格（命令已被接受或开始填参数），
// 前缀匹配自然失败，Tab 回退为缩进、Enter 恢复提交。
func (m *Model) refreshCompletionLocked() {
	raw := m.editor.Text()
	if !strings.HasPrefix(raw, "/") {
		m.completionOverlay = false
		return
	}
	if m.completion == nil {
		cands := make([]CompletionCandidate, 0, len(m.commandInfo))
		for _, info := range m.commandInfo {
			cands = append(cands, CompletionCandidate{
				Command:     info.Command,
				Description: info.Description,
			})
		}
		m.completion = NewCompletionState(cands)
	}
	m.completion.Query(strings.TrimPrefix(raw, "/"))
	m.commandsCursor = 0
	m.commandsScroll = 0
	// 输入 `/` 且仍有候选时自动弹出浮层，不必再按 Ctrl+O。
	// 侧栏的 showCommands 语义保持不变。
	m.completionOverlay = m.completion.Count() > 0
}

// acceptCompletionLocked 把当前选中的候选写入输入行。补上一个尾随空格，
// 使下一次按键重算 query 后候选数归零——于是 Enter 提交、Tab 回退为缩进，
// 不会卡在"反复接受同一候选"的循环里。接受动作永不提交回合。
func (m *Model) acceptCompletionLocked() KeyAction {
	c, ok := m.completion.Current()
	if !ok {
		return ActionNone
	}
	m.editor.SetText("/" + c.Command + " ")
	m.refreshCompletionLocked()
	return ActionNone
}

// openExternalEditorLocked 通过 $VISUAL / $EDITOR 编辑当前提示。
// 提示被写入临时文件，编辑器独占使用
// 终端，然后把文件读回提示。每条失败
// 路径都是空操作：原始文本保持不变，循环永不中断。
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

// filterKeyLocked 在会话过滤栏持有键盘时路由按键。
// 过滤是实时的：每次按键都重新渲染侧边栏。Esc 取消并
// 清空查询，Enter 确认并保留，Ctrl+L 离开焦点而不
// 改变查询。此模式下任何按键都不会被转发到提示。
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
		// 无事可做
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
	m.completionOverlay = false // 提交后输入已清空，浮层必须立即消失
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

// requestActionLocked 向 run 循环报告一个动作。未安装的钩子
// 让命令保持可发现，但绝不假装它做了什么。
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

// handleVimKeyLocked 处理 vim 键位。返回 true 表示键已被消费（调用方直接
// 返回 ActionNone），false 表示交还给主 switch 按经典行为处理。
//
// 文本与光标的单位不同：Editor.Cursor() 是 rune 索引，VimMotion 用字节索引。
// 这里在两者间以完整 UTF-8 字符边界换算，保证 CJK/emoji 不被从中间切开。
func (m *Model) handleVimKeyLocked(key Key) bool {
	text := m.editor.Text()
	if m.vimInsert {
		if key.Kind == KindEsc {
			// vim 惯例：退出插入态时光标左移一格（钳制到合法边界）。
			m.vimInsert = false
			c := m.editor.Cursor()
			if c > 0 {
				m.editor.SetCursor(c - 1)
			}
			return true
		}
		// 插入态其余键交还主 switch，逐字节保持经典输入行为。
		return false
	}

	// normal 态：仅处理可打印字符作为 vim 命令；其它 Kind（Tab/Esc/方向键/
	// Ctrl 组合）交还主 switch，保持补全、面板与审批的经典语义。
	if key.Kind != KindRune {
		return false
	}
	k := string(key.Rune)
	// i/a/I/A 进入插入态；其中 a/A 按 vim 惯例光标后移一格，I 定位行首非空白。
	enterInsert := false
	switch k {
	case "i":
		enterInsert = true
	case "a":
		enterInsert = true
		k = "l" // 先右移一格再进入插入态
	case "I":
		enterInsert = true
		k = "^" // 行首首个非空白
	case "A":
		enterInsert = true
		k = "$" // 行尾
	}

	// rune 索引 → 字节索引：统计前 c 个 rune 的字节长度。
	runes := []rune(text)
	c := m.editor.Cursor()
	if c < 0 {
		c = 0
	}
	if c > len(runes) {
		c = len(runes)
	}
	byteIdx := len(string(runes[:c]))

	_, newByte, handled := VimMotion(text, byteIdx, k)
	if enterInsert {
		// i/I 定位后直接进入插入态；a/A 的右移已含在 k 中。
		m.vimInsert = true
	}
	if !handled && !enterInsert {
		// 未识别的 motion 键：normal 态消费但不插入字符（vim 的"无效命令"语义），
		// 光标保持原合法边界。
		return true
	}
	// 字节索引 → rune 索引（VimMotion 保证落点在合法 UTF-8 边界）。
	if newByte < 0 {
		newByte = 0
	}
	if newByte > len(text) {
		newByte = len(text)
	}
	m.editor.SetCursor(utf8.RuneCountInString(text[:newByte]))
	return true
}
