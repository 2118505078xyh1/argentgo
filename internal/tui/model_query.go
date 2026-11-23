package tui

import (
	"context"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// MarkDirty 记录已渲染内容发生变化、下一帧必须
// 重绘。从任何 goroutine 调用都是安全的。
func (m *Model) MarkDirty() {
	m.mu.Lock()
	m.markDirtyLocked()
	m.mu.Unlock()
}

func (m *Model) markDirtyLocked() {
	m.dirty = true
}

// Dirty 报告自上次绘制以来内容是否变化。
func (m *Model) Dirty() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.dirty
}

// ClearDirty 确认一次绘制。大多数调用方从不需要它：Paint 会
// 自行清除该标志。
func (m *Model) ClearDirty() {
	m.mu.Lock()
	m.dirty = false
	m.mu.Unlock()
}

// SetTheme 在运行时切换调色板。未知名称保持当前
// 主题不变，并返回包装了 ErrUnknownTheme 的错误。
func (m *Model) SetTheme(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.applyThemeLocked(name)
}

// Theme 是活动主题的规范名称。
func (m *Model) Theme() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.theme
}

// Palette 返回当前驱动渲染器的调色板。
func (m *Model) Palette() Palette {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.palette
}

// SetNoColor 切换颜色抑制，而无需重建模型。
func (m *Model) SetNoColor(v bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.noColor == v {
		return
	}
	m.noColor = v
	_ = m.applyThemeLocked(m.theme)
}

// NoColor 报告颜色 SGR 码是否被抑制。
func (m *Model) NoColor() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.noColor
}

// Degraded 报告注入的环境是否要求单色。
func (m *Model) Degraded() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.degraded
}

// applyThemeLocked 根据主题、环境和
// NoColor 标志重建调色板。它总是提升版本，使重绘采纳变更。
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

// SetSessions 替换会话栏。空切片隐藏侧边栏，但
// 保留之前选中的行供下次填充使用。
func (m *Model) SetSessions(rows []SessionInfo) {
	m.mu.Lock()
	m.sessions = append([]SessionInfo{}, rows...)
	m.sessionsCursor = clampScroll(m.sessionsCursor, maxInt(len(m.sessions)-1, 0))
	m.version++
	m.markDirtyLocked()
	m.mu.Unlock()
}

// Sessions 返回会话栏各行的一个副本。
func (m *Model) Sessions() []SessionInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]SessionInfo{}, m.sessions...)
}

// SetCommandInfo 替换斜杠命令元数据，并重建补全面板使用的候选状态。
// 镜像 SetSessions：持锁、拷贝入参、提升版本并标脏。空切片产生一个合法
// 但无候选的补全状态，Tab 因此回退为插入两个空格。
// 它不打开面板——showCommands 只能由 Ctrl+O 切换。
// SetVimMode 启用或关闭输入行的 vim 键位。默认（未调用或传 false）时保持经典
// 编辑行为，逐字节不变；开启后 normal 态的字母键走 VimMotion，i/a/I/A 进入插入态。
func (m *Model) SetVimMode(on bool) {
	m.mu.Lock()
	m.vimMode = on
	m.vimInsert = false
	m.mu.Unlock()
}

func (m *Model) SetCommandInfo(rows []CommandInfo) {
	m.mu.Lock()
	// 补全列表 = TUI 原生命令（/help /clear /undo …）+ 宿主注入的命令。
	// 没有这一步，输入 `/` 只能看到 internal/commands 的两个内置命令。
	merged := mergeCommandInfo(m.locale, rows)
	m.commandInfo = merged
	cands := make([]CompletionCandidate, 0, len(merged))
	for _, row := range merged {
		cands = append(cands, CompletionCandidate{
			Command:     row.Command,
			Description: row.Description,
		})
	}
	m.completion = NewCompletionState(cands)
	m.commandsCursor = clampScroll(m.commandsCursor, maxInt(len(merged)-1, 0))
	m.commandsScroll = 0
	m.version++
	m.markDirtyLocked()
	m.mu.Unlock()
}

// SetTurnActions 安装 undo / retry / rename 接缝。传入 nil 清除
// 某个动作，使其斜杠命令以通知作答。
func (m *Model) SetTurnActions(undo, retry, rename TurnAction) {
	m.mu.Lock()
	m.undoFn, m.retryFn, m.renameFn = undo, retry, rename
	m.mu.Unlock()
}

// TakeActionArg 返回最后一次产生动作的命令的参数。
func (m *Model) TakeActionArg() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	arg := m.actionArg
	m.actionArg = ""
	return arg
}

// SetApproval 安装注入的审批权威。它在
// 操作员按下 y/a 后被咨询；nil 函数表示答案即为最终结果。
func (m *Model) SetApproval(fn func(ctx context.Context, name, args string) (bool, error)) {
	m.mu.Lock()
	m.approvalFn = fn
	m.mu.Unlock()
}

// Quit 报告操作员是否要求离开。
func (m *Model) Quit() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.quit
}

// Busy 报告是否正在运行某个回合。
func (m *Model) Busy() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.working
}

// Input 是当前提示文本。
func (m *Model) Input() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.editor.Text()
}

// Cursor 是以 rune 索引表示的光标位置。
func (m *Model) Cursor() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.editor.Cursor()
}

// Transcript 是当前各块的一个副本。
func (m *Model) Transcript() []Entry {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.flushStreamLocked()
	return append([]Entry{}, m.entries...)
}

// TakeSubmitted 返回最后一次提交的提示文本并清空它。
func (m *Model) TakeSubmitted() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	text := m.submitted
	m.submitted = ""
	return text
}

// setUsageLocked 是 SetUsage 的持锁实现，供已持有 m.mu 的调用方
// （如 HandleEvent）复用，避免对不可重入的 sync.Mutex 再次加锁。
func (m *Model) setUsageLocked(prompt, completion int) {
	m.usageIn, m.usageOut = prompt, completion
	m.markDirtyLocked()
}

// setCachedTokensLocked 是 SetCachedTokens 的持锁实现，与
// setUsageLocked 同理，供 HandleEvent 在已持锁时调用。
func (m *Model) setCachedTokensLocked(cached int) {
	m.usageCached = cached
	m.markDirtyLocked()
}

// SetUsage 记录用于头部的 token 总数。
func (m *Model) SetUsage(prompt, completion int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.setUsageLocked(prompt, completion)
}

// SetCachedTokens 记录提示 token 中的缓存前缀部分。
func (m *Model) SetCachedTokens(cached int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.setCachedTokensLocked(cached)
}

// Plain 把记录渲染为无样式文本，用于离开备用屏幕后
// 仍留在终端中的回滚浏览。
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

// plainOutputPreview 让回滚保持简短。完整输出已经
// 存在于 Entry.Output 中供交互式视图使用，但退出时倾倒它会
// 淹没终端；这里只返回有界的头部。
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
	// 回合结束的错误可能仍携带原始 provider 消息（agent 也
	// 会发布清洗后的 EventError）。清洗、折叠为一行并限制长度，使
	// 凭据或多行转储无法泄漏进记录或将其撑宽。
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

// visibleWindow 返回一个滚动列表应渲染的半开项区间 [start, end)：
// 以 scrollOffset 开始、跨 viewportHeight 个项的视口，两侧各扩展 overscan 个项，
// 使视图外紧邻的行在操作员滚动时预热。
// 即视图外紧邻的行在滚动时预热。
//
// 这是一个纯整数辅助函数（它统计项而非字节或 rune，因此 CJK 和
// 折叠后的显示宽度在这里无关），且对越界输入安全：负数 total 产生空窗口，
// 偏移被钳制到 [0, total]，非正的 viewport 在偏移处产生空窗口，
// 且两端始终保持在 [0, total] 内
// 并满足 start <= end。
//
// 这是在 bodyLinesLocked 中对记录做窗口化的可复用基础；
// 把它接线进去有意留给
// render.go 的负责人。
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
	// 大于列表长度的值不会扩大钳制后的窗口，因此在这里限制
	// 可防止下面的加法在极端输入下溢出。
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
