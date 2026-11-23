package tui

import (
	"os"
	"os/exec"
	"strings"
	"time"
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
	// 可选别名旁路（opt-in）：仅对主 switch 未覆盖的可打印键生效，
	// 已有专属 Kind 的绑定优先。无配置时别名表为空、守卫短路，
	// 以下主 switch 与帧字节保持逐字节不变。
	if action, ok := m.tryAliasKeyLocked(key); ok {
		return m.applyAliasActionLocked(action)
	}
	// kill-ring 连续删除串：只有删除/召回键延续当前串，光标移动与文本插入都断开它。
	switch key.Kind {
	case KindBackspace, KindDelete, KindCtrlK, KindCtrlU, KindCtrlW, KindCtrlY, KindAltY:
	default:
		m.kill.BreakChain()
	}
	switch key.Kind {
	case KindIgnore:
		return ActionNone
	case KindCtrlL:
		// pi 把 Ctrl+L 作为模型选择器。运行期换模型会牵动 provider、
		// 系统提示与工具集，TUI 不假装能就地切换：显示当前模型和
		// 重启时的 --model 用法，与 /models 完全等价。
		m.appendNoticeLocked(m.modelsInfoLocked())
	case KindAltT:
		m.showTasks = !m.showTasks
		m.version++
		if m.showTasks {
			m.setStatusLocked(text(m.locale, "tasks_on"))
		} else {
			m.setStatusLocked(text(m.locale, "tasks_off"))
		}
	case KindAltB:
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
	case KindAltP:
		m.showPlan = !m.showPlan
		m.version++
		if m.showPlan {
			m.setStatusLocked(text(m.locale, "plan_on"))
		} else {
			m.setStatusLocked(text(m.locale, "plan_off"))
		}
	case KindAltS:
		m.showSessions = !m.showSessions
		m.version++
		if m.showSessions {
			m.setStatusLocked(text(m.locale, "sessions_on"))
		} else {
			m.setStatusLocked(text(m.locale, "sessions_off"))
		}
	case KindAltC:
		m.showCommands = !m.showCommands
		m.version++
		if m.showCommands {
			m.setStatusLocked(text(m.locale, "commands_on"))
		} else {
			m.setStatusLocked(text(m.locale, "commands_off"))
		}
	case KindCtrlT:
		// pi: ctrl+t 折叠/展开思考块。
		m.toggleReasoningAllLocked()
	case KindCtrlO:
		// pi: ctrl+o 折叠/展开工具输出，与 /expand 共用同一个开关。
		m.expanded = !m.expanded
		m.version++
		m.markDirtyLocked()
		if m.expanded {
			m.setStatusLocked(text(m.locale, "expanded"))
		} else {
			m.setStatusLocked(text(m.locale, "collapsed"))
		}
	case KindCtrlX:
		// pi: ctrl+x 复制最后一条助手消息/当前选区。
		m.copyLastAnswerLocked()
	case KindCtrlP:
		// pi: ctrl+p 切换模型。此处与 /models 一致，只显示当前模型，
		// 不伪造一个无法落地的运行期切换。
		m.appendNoticeLocked(m.modelsInfoLocked())
	case KindCtrlN:
		m.sessionNamedOnly = !m.sessionNamedOnly
		m.sessionsScroll = 0
		m.version++
		if m.sessionNamedOnly {
			m.setStatusLocked(text(m.locale, "named_on"))
		} else {
			m.setStatusLocked(text(m.locale, "named_off"))
		}
	case KindCtrlR:
		if m.showSessions && len(m.sessions) > 0 {
			m.renamePending = true
			m.setStatusLocked(text(m.locale, "session_rename_prompt"))
			return ActionNone
		}
		m.setStatusLocked(text(m.locale, "name_usage"))
	case KindCtrlV:
		// 终端的括号粘贴走 KindPaste；这里没有独立的剪贴板读取 API，
		// 与 pi 的“粘贴图片”语义对齐为不打断输入。
		return ActionNone
	case KindAltL:
		if m.showSessions {
			m.sessionFiltering = !m.sessionFiltering
		}
	case KindCtrlHome:
		m.scrollByLocked(-1 << 20)
	case KindCtrlEnd:
		m.scrollByLocked(1 << 20)
	case KindCtrlG:
		return m.openExternalEditorLocked()
	case KindAltU:
		return m.requestActionLocked(ActionUndo, "")
	case KindAltR:
		return m.requestActionLocked(ActionRetry, "")
	case KindCtrlC:
		if m.working {
			// 工作态的 Ctrl+C 始终是取消，不走二次确认。
			m.setStatusLocked(text(m.locale, "cancelling"))
			m.disarmQuitLocked()
			return ActionAbort
		}
		if strings.TrimSpace(m.editor.Text()) != "" {
			// 先清输入行；这与经典的 kill 语义一致，且此时不应武装退出。
			m.editor.Clear()
			m.disarmQuitLocked()
			return ActionNone
		}
		now := m.now()
		if !m.quitArmedAt.IsZero() && now.Sub(m.quitArmedAt) <= quitConfirmWindow {
			m.quit = true
			return ActionQuit
		}
		// 空闲第一次：显示"再按一次退出"，3 秒内第二次才真正退出。
		m.quitArmedAt = now
		m.setStatusLocked(tuiText(m.locale, "quit_armed"))
		return ActionNone
	case KindCtrlD:
		// pi 的会话列表里 Ctrl+D 删除当前会话。
		if m.showSessions && len(m.sessions) > 0 && !m.sessionFiltering {
			return m.requestActionLocked(ActionDeleteSession, m.sessions[m.sessionsCursor].Path)
		}
		// pi: 输入非空时前删一个字符；输入为空时直接退出。
		if strings.TrimSpace(m.editor.Text()) == "" {
			m.quit = true
			return ActionQuit
		}
		deleted := m.killOneForwardLocked()
		m.editor.Delete()
		if deleted != "" {
			m.kill.Kill(deleted, killForward, true)
		}
	case KindCtrlS:
		// pi: 会话列表切换排序。这里反转顺序并保持选中项不变。
		for i, j := 0, len(m.sessions)-1; i < j; i, j = i+1, j-1 {
			m.sessions[i], m.sessions[j] = m.sessions[j], m.sessions[i]
		}
		m.sessionsCursor = len(m.sessions) - 1 - m.sessionsCursor
		m.sessionsScroll = 0
		m.version++
		m.setStatusLocked(text(m.locale, "sort_toggled"))
	case KindEsc:
		if m.renamePending {
			m.renamePending = false
			m.setStatusLocked("")
		}
		if m.working {
			m.setStatusLocked(text(m.locale, "cancelling"))
			m.disarmQuitLocked()
			return ActionAbort
		}
		// 逐层关闭：浮层 → 面板 → 补全。一次按键只关一层，
		// 下一次输入会按新文本重算 query 重新打开补全。
		m.closeTopLayerLocked()
		return ActionNone
	case KindEnter:
		// 候选可见时 Enter 接受候选项，而不是提交整行。
		if m.completionActiveLocked() {
			return m.acceptCompletionLocked()
		}
		// 会话栏获得焦点的 Enter 打开选中会话；改名待确认时把输入行
		// 当作新标题。这样 /sessions 不再只是“看一眼”的面板。
		if m.showSessions && len(m.sessions) > 0 && !m.sessionFiltering {
			row := m.sessions[m.sessionsCursor].Path
			if m.renamePending {
				title := strings.TrimSpace(m.editor.Text())
				m.renamePending = false
				m.editor.Clear()
				if title == "" {
					m.setStatusLocked(text(m.locale, "name_usage"))
					return ActionNone
				}
				return m.requestActionLocked(ActionRenameSession, row+"\x00"+title)
			}
			return m.requestActionLocked(ActionOpenSession, row)
		}
		return m.submitLocked()
	case KindAltEnter, KindNewline:
		m.editor.Insert("\n")
		return ActionNone
	case KindBackspace:
		deleted := m.killOneBackwardLocked()
		m.editor.Backspace()
		if deleted != "" {
			m.kill.Kill(deleted, killBackward, true)
		}
		m.refreshCompletionLocked()
	case KindDelete:
		deleted := m.killOneForwardLocked()
		m.editor.Delete()
		if deleted != "" {
			m.kill.Kill(deleted, killForward, true)
		}
	case KindLeft, KindCtrlB:
		m.editor.Left()
	case KindRight, KindCtrlF:
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
		deleted := m.killToEndLocked()
		m.editor.KillToEnd()
		if deleted != "" {
			m.kill.Kill(deleted, killForward, true)
		}
	case KindCtrlU:
		deleted := m.killToStartLocked()
		m.editor.KillToStart()
		if deleted != "" {
			m.kill.Kill(deleted, killBackward, true)
		}
	case KindCtrlW:
		deleted := m.killWordLocked()
		m.editor.KillWord()
		if deleted != "" {
			m.kill.Kill(deleted, killBackward, true)
		}
	case KindCtrlY:
		// yank：把最近一条（或 yank-pop 循环的当前条）插回光标处。
		text := m.kill.Yank()
		if text != "" {
			m.editor.Insert(text)
			m.refreshCompletionLocked()
		}
	case KindAltY:
		// yank-pop：在环内循环取回更早的删除片段。
		text := m.kill.YankPop()
		if text != "" {
			m.editor.Insert(text)
			m.refreshCompletionLocked()
		}
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
		text := key.Text
		// 粘贴的若是整段已存在的文件路径（WT 引号 / Unix 转义 / Rio NUL），
		// 改写为 @引用再写入输入框；否则逐字节保留原始粘贴内容。默认开启，
		// 仅 ARGENT_PASTE_FILE_MENTIONS=0 时回退到旧行为。
		if m.pasteMentionsEnabled() {
			if paths, ok := ParsePastedFiles(text, pastePathStat); ok {
				text = PastedFilesToMentions(m.root, paths)
			}
		}
		m.editor.Insert(text)
		m.refreshCompletionLocked()
	}
	return ActionNone
}

// quitConfirmWindow 是空闲 Ctrl+C 二次确认退出的有效窗口。
const quitConfirmWindow = 3 * time.Second

// disarmQuitLocked 取消待确认的退出状态。任何提交、编辑或取消动作都会
// 让它失效，避免过期的武装状态在之后误触发退出。调用方持有 mu。
func (m *Model) disarmQuitLocked() {
	m.quitArmedAt = time.Time{}
}

// closeTopLayerLocked 按「浮层 → 面板 → 补全」顺序关闭一层，
// 返回是否真的关掉了什么。调用方持有 mu。
func (m *Model) closeTopLayerLocked() bool {
	// 浮层：输入 / 后自动弹出的补全浮层。
	if m.completionOverlay {
		m.completionOverlay = false
		m.commandsCursor = 0
		m.commandsScroll = 0
		return true
	}
	// 面板：一次只关一个，按打开频率从高到低。
	if m.showCommands {
		m.showCommands = false
		m.version++
		m.setStatusLocked(text(m.locale, "commands_off"))
		return true
	}
	if m.showTasks {
		m.showTasks = false
		m.version++
		m.setStatusLocked(text(m.locale, "tasks_off"))
		return true
	}
	if m.showWorkspace {
		m.showWorkspace = false
		m.version++
		m.setStatusLocked(text(m.locale, "workspace_off"))
		return true
	}
	if m.showPlan {
		m.showPlan = false
		m.version++
		m.setStatusLocked(text(m.locale, "plan_off"))
		return true
	}
	if m.showSessions {
		m.showSessions = false
		m.version++
		m.setStatusLocked(text(m.locale, "sessions_off"))
		return true
	}
	// 补全：清除残留的候选状态与命令游标。
	if m.completion != nil {
		m.completion = nil
		m.commandsCursor = 0
		m.commandsScroll = 0
		return true
	}
	// 没有可关的层：沿用旧的滚动复位，保持无面板场景的行为不变。
	m.scroll = 0
	return false
}

// pastePathStat 是粘贴路径探测的可替换后端。测试注入假 stat，不碰真实磁盘。
var pastePathStat PathStat = os.Stat

// pasteMentionsEnabled 报告粘贴文件路径识别是否开启。默认开启，
// 只有 ARGENT_PASTE_FILE_MENTIONS=0 时才回退到旧的整块粘贴行为。
func (m *Model) pasteMentionsEnabled() bool {
	return m.env == nil || strings.TrimSpace(m.env("ARGENT_PASTE_FILE_MENTIONS")) != "0"
}

// killRingEnabled 报告 kill-ring 是否开启。默认开启，
// 只有 ARGENT_KILL_RING=0 时删除键照常工作但不记入环。
func (m *Model) killRingEnabled() bool {
	return m.env == nil || strings.TrimSpace(m.env("ARGENT_KILL_RING")) != "0"
}

// 以下辅助在删除前从编辑器取出将被删掉的 rune 片段。它们返回的是真正会从
// 缓冲区消失的文本，因此记入 kill-ring 的内容与用户看到的一致；按 rune 切片
// 保证 CJK/emoji 不会被切坏。

func (m *Model) killOneBackwardLocked() string {
	if !m.killRingEnabled() {
		return ""
	}
	runes, cur := m.editor.Runes(), m.editor.Cursor()
	if cur > 0 {
		return string(runes[cur-1])
	}
	return ""
}

func (m *Model) killOneForwardLocked() string {
	if !m.killRingEnabled() {
		return ""
	}
	runes, cur := m.editor.Runes(), m.editor.Cursor()
	if cur < len(runes) {
		return string(runes[cur])
	}
	return ""
}

func (m *Model) killToEndLocked() string {
	if !m.killRingEnabled() {
		return ""
	}
	runes, cur := m.editor.Runes(), m.editor.Cursor()
	if cur < len(runes) {
		return string(runes[cur:])
	}
	return ""
}

func (m *Model) killToStartLocked() string {
	if !m.killRingEnabled() {
		return ""
	}
	runes, cur := m.editor.Runes(), m.editor.Cursor()
	if cur > 0 {
		return string(runes[:cur])
	}
	return ""
}

func (m *Model) killWordLocked() string {
	if !m.killRingEnabled() {
		return ""
	}
	runes, cur := m.editor.Runes(), m.editor.Cursor()
	start := cur
	for start > 0 && runes[start-1] == ' ' {
		start--
	}
	for start > 0 && runes[start-1] != ' ' && runes[start-1] != '\n' {
		start--
	}
	if start == cur {
		return ""
	}
	return string(runes[start:cur])
}

// pathCompletionEnabled 报告内联路径 / @提及补全是否开启。默认开启：
// m.env 为 nil 或 ARGENT_PATH_COMPLETION 修剪后不等于 "0" 时返回 true，
// 只有修剪后精确等于 "0" 才关闭。env 由集成方注入（run.go 用 os.Getenv），
// TUI 自身的渲染路径绝不直接读环境变量。
func (m *Model) pathCompletionEnabled() bool {
	return m.env == nil || strings.TrimSpace(m.env("ARGENT_PATH_COMPLETION")) != "0"
}

// completionActiveLocked 报告输入行是否应由补全接管。
// slash 补全要求文本以 '/' 开头；内联补全由 CompletionState.inline 标识，
// 只要过滤后仍有候选即接管。关闭 opt-in 时 inline 恒为 false，行为不变。
func (m *Model) completionActiveLocked() bool {
	if m.completion == nil || m.completion.Count() == 0 {
		return false
	}
	if m.completion.inline {
		return true
	}
	return strings.HasPrefix(m.editor.Text(), "/")
}

// refreshCompletionLocked 在文本变化后重算查询。Esc 关闭补全时把状态置空，
// 这里按需从 commandInfo 重建，使再次输入可以重新打开补全。
// 查询取 '/' 后的整段文本：一旦输入空格（命令已被接受或开始填参数），
// 前缀匹配自然失败，Tab 回退为缩进、Enter 恢复提交。
func (m *Model) refreshCompletionLocked() {
	raw := m.editor.Text()
	// opt-in：先尝试内联（路径 / @文件）补全；门禁关闭时 tryInline 整段跳过，
	// 下面的 slash 逻辑与接入前逐字节一致。
	if m.pathCompletionEnabled() && m.tryInlineCompletionLocked(raw) {
		return
	}
	// 未被内联补全接管（含门禁关闭）：清掉上一次留下的内联状态，避免
	// 无候选的路径文本仍保留"幽灵"激活态。门禁关闭时 inline 恒为 false，
	// 这是无操作，故既有 slash 行为逐字节不变。
	if m.completion != nil && m.completion.inline {
		m.completion = nil
	}
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

// tryInlineCompletionLocked 在 opt-in 开启时尝试为光标前的词构建内联补全。
// 返回 true 表示本次文本变化已被内联补全接管且确有候选，调用方不应再走
// slash 分支。非内联上下文（含行首 slash 命令）或无候选时返回 false。
func (m *Model) tryInlineCompletionLocked(raw string) bool {
	word, start, quoted := activeInlineWord(raw, m.editor.Cursor())
	kind := classifyInlineWord(word, start)
	if kind == inlineNone {
		return false
	}

	var cands []CompletionCandidate
	var query string
	switch kind {
	case inlineAt:
		cands = atCompletionCandidates(m.fs, m.root)
		query = strings.TrimPrefix(word, "@")
		// 保留 '@' 作为前缀：只替换它后面的提及词，接受后得到 "@README.md"。
		start++
	case inlinePath:
		cands = pathCompletionCandidates(m.fs, m.root, word)
		query = word
	}
	if len(cands) == 0 {
		// 有内联意图但无候选：保持关闭，交回 slash 兜底由调用方处理。
		return false
	}

	m.completion = NewCompletionState(cands)
	m.completion.inline = true
	m.completion.tokenStart = start
	m.completion.cursor = m.editor.Cursor()
	m.completion.quoted = quoted
	m.completion.Query(query)
	m.commandsCursor = 0
	m.commandsScroll = 0
	m.completionOverlay = m.completion.Count() > 0
	return true
}

// acceptCompletionLocked 把当前选中的候选写入输入行。补上一个尾随空格，
// 使下一次按键重算 query 后候选数归零——于是 Enter 提交、Tab 回退为缩进，
// 不会卡在"反复接受同一候选"的循环里。接受动作永不提交回合。
func (m *Model) acceptCompletionLocked() KeyAction {
	c, ok := m.completion.Current()
	if !ok {
		return ActionNone
	}
	if m.completion.inline {
		return m.acceptInlineCompletionLocked(c)
	}
	m.editor.SetText("/" + c.Command + " ")
	m.refreshCompletionLocked()
	return ActionNone
}

// acceptInlineCompletionLocked 只替换光标前正在补全的那个词，保留输入行
// 其余部分（含光标后文本）。候选保存的是原始相对路径：若词不在引号内且
// 路径含空格，写入时由 quoteIfNeeded 包裹；已在引号内则沿用原有引号。
// 补一个尾随空格使下一次 refresh 的词为空——补全自然关闭，不会反复接受
// 同一候选。
func (m *Model) acceptInlineCompletionLocked(c CompletionCandidate) KeyAction {
	runes := []rune(m.editor.Text())
	start, cursor := m.completion.tokenStart, m.completion.cursor
	if start < 0 {
		start = 0
	}
	if cursor > len(runes) {
		cursor = len(runes)
	}
	if start > cursor {
		start = cursor
	}
	path := c.Command
	if !m.completion.quoted {
		path = quoteIfNeeded(path)
	}
	insert := path + " "
	next := string(runes[:start]) + insert + string(runes[cursor:])
	m.editor.SetText(next)
	m.editor.SetCursor(start + utf8.RuneCountInString(insert))
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
	case KindCtrlL, KindAltL:
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
	m.disarmQuitLocked()
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

// copyLastAnswerLocked 复制最后一条助手正文到剪贴板（pi 的 ctrl+x）。
func (m *Model) copyLastAnswerLocked() {
	for i := len(m.entries) - 1; i >= 0; i-- {
		if m.entries[i].Kind != EntryAssistant || strings.TrimSpace(m.entries[i].Text) == "" {
			continue
		}
		if err := CopyToClipboard(m.entries[i].Text); err != nil {
			m.setStatusLocked(text(m.locale, "copy_failed"))
		} else {
			m.setStatusLocked(text(m.locale, "copied"))
		}
		return
	}
	m.setStatusLocked(text(m.locale, "nothing_to_copy"))
}

func (m *Model) commandLocked(body string) KeyAction {
	name := body
	arg := ""
	if i := strings.IndexAny(body, " \t"); i >= 0 {
		name = body[:i]
		arg = strings.TrimSpace(body[i+1:])
	}
	// 命令执行体集中在 nativeCommands 注册表里；这里只负责分发。
	if cmd, ok := lookupNativeCommand(strings.TrimPrefix(name, "/")); ok {
		return cmd.Run(m, arg)
	}
	// 未注册的命令交给宿主（例如 /context、/review 或 markdown 命令）。
	m.appendUserLocked(body)
	m.working = true
	m.scroll = 0
	m.submitted = body
	return ActionSubmit
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
	case ActionCompact:
		hook, key = m.compactFn, "compact_unwired"
	case ActionOpenSession:
		hook, key = m.openSessionFn, "session_open_unwired"
	case ActionDeleteSession:
		hook, key = m.deleteSessionFn, "session_delete_unwired"
	case ActionRenameSession:
		hook, key = m.renameSessionFn, "session_rename_unwired"
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
