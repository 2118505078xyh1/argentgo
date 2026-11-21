package tui

import (
	"context"
	"strings"
	"sync"
	"time"
)

// EntryKind 对一个记录块分类。
type EntryKind int

// 记录块种类。
const (
	EntryUser EntryKind = iota
	EntryAssistant
	EntryReasoning
	EntryTool
	EntryNotice
)

// AttemptKind 是附着在记录块上的未提交结果。
// 它仅是展示元数据：agent 历史永远看不到这些标签。
type AttemptKind string

// UI 可以加徽章的尝试结果。
const (
	AttemptFailed      AttemptKind = "failed"
	AttemptRetried     AttemptKind = "retried"
	AttemptCancelled   AttemptKind = "cancelled"
	AttemptStreamError AttemptKind = "stream_error"
)

// Entry 是一个记录块。助手文本在流式传输时累积；
// 工具条目携带调用和截断的输出。失败/重试/取消的行
// 会设置 Attempt，使操作员一眼可见。
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

	// 推理块的仅用于展示的计时。Entry 没有公开的 time
	// 字段；这些值在首次绘制时由 reasoningLines 打上，仅用于
	// 构建单行摘要，因此不改变公共 API 或事件。
	thinkingAt  time.Time
	thinkingEnd time.Time
}

// SessionInfo 是可选会话栏的一行。Title 是生成的
// （或显式重命名的）标题；Path 保留给提示和钩子使用。
type SessionInfo struct {
	Path  string
	Title string
}

// CommandInfo 是宿主注入的一条斜杠命令元数据。tui 不依赖
// internal/commands（依赖规则禁止反向引用上层包），由集成方把命令的
// 名称、描述与参数提示转换成这个窄类型。
type CommandInfo struct {
	Command      string
	Description  string
	ArgumentHint string
}

// BufferPos 是已渲染缓冲区中基于 0 的 (row, col) 位置。鼠标
// 报告基于 1；run 循环在调用
// 选择方法前减一。
type BufferPos struct {
	Row int
	Col int
}

// TurnAction 是 undo / retry / rename 的窄接缝。TUI 从不
// 导入 session 或 app 包；由集成方提供行为。
type TurnAction func(ctx context.Context, arg string) error

// Config 为 Model 提供种子。
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

// KeyAction 告诉事件循环，模型更新后某个按键请求什么。
type KeyAction int

// 一个按键可以产生的动作。
const (
	ActionNone KeyAction = iota
	ActionSubmit
	ActionAbort
	ActionQuit
	ActionUndo
	ActionRetry
	ActionRename
)

// entryCacheKey 捕获改变某个记录条目渲染方式的所有输入。
// 按条目保留它（而非整个正文一个键），
// 使流式增量或工具状态变化只重绘单个块。
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

// entryCacheSlot 是与 Model.entries 按下标对齐的一个已渲染条目。
type entryCacheSlot struct {
	key   entryCacheKey
	lines []Line
}

// Model 是整个 UI 状态。它不拥有终端：每个方法都可以由
// 测试驱动，且只有 run 循环触碰屏幕。
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

	scroll      int
	bodyH       int
	bodyW       int
	bodyLines   int // rendered transcript line count cached by the last Paint
	bodyVersion int // model version at that paint, used to detect stale counts
	version     int
	entryCache  []entryCacheSlot

	// notices 是按严重度排序的状态通知栈。新的状态
	// 消息进入这里；下面两个旧字段仅为让仍直接写它们的
	// 代码继续工作而保留（栈为空时 statusLineLocked 回退到
	// 它们）。
	notices NoticeStack

	// 基于 0 的缓冲区坐标中的文本选择。从 MouseDown 到 MouseUp
	// selecting 为 true；anchor 和 active 在 MouseUp 后保留，使该范围
	// 之后仍可被复制。
	selAnchor BufferPos
	selActive BufferPos
	selecting bool

	// dirty 记录两次绘制之间的内容变化。run 循环以此门控
	// 重绘；缓冲区差异仍是
	// 实际写入终端内容的最终权威。
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

	// 可选的头部元数据。两者都未设置时，头部保持这些字段
	// 出现之前的经典样式。
	contextWindow int
	cost          string

	// 动作钩子由集成方安装。nil 表示该命令被识别但会以清晰的
	// "未接线"通知作答。
	undoFn    TurnAction
	retryFn   TurnAction
	renameFn  TurnAction
	actionArg string

	// 可选的侧边栏。三者默认全部隐藏，使经典
	// 记录视图及其按键绑定逐字节不变。
	showTasks      bool
	showWorkspace  bool
	showPlan       bool
	showSessions   bool
	sessionsCursor int
	sessionsScroll int
	// sessionFilter 是显示在会话行上方的已解析模糊查询；
	// sessionFiltering 在该过滤栏持有键盘时为 true。
	// 两者在会话栏显示前都是无效果的。
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

	// 斜杠命令补全面板。三者（showCommands/completion 指针/候选数）默认
	// 全为零值，使经典记录视图、Tab 插两空格与默认帧逐字节不变。
	showCommands   bool
	commandInfo    []CommandInfo
	commandsCursor int
	commandsScroll int
	completion     *CompletionState

	pending       *approvalRequest
	approvalQueue []approvalRequest
	allowed       map[string]bool
	approvalFn    func(context.Context, string, string) (bool, error)

	now func() time.Time
}

// New 返回一个可渲染的模型。空的 Theme 选择深色
// 调色板，使早于主题功能的调用方保持不变。
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

// SetContextInfo 提供可选的头部元数据：模型的 context
// 窗口（用于用量百分比）和预格式化的成本字符串。传入 0
// 和 "" 恢复原始头部。在 run 循环绘制时调用是安全的。
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

// MouseDown 在基于 0 的缓冲区位置开始选择。它替换任何
// 先前的选择。
func (m *Model) MouseDown(col, row int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.selAnchor = BufferPos{Row: row, Col: col}
	m.selActive = m.selAnchor
	m.selecting = true
	m.markDirtyLocked()
}

// MouseDrag 扩展当前选择。没有活动 MouseDown 的拖拽被
// 忽略，因此杂散的移动报告无法创建选择。
func (m *Model) MouseDrag(col, row int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.selecting {
		return
	}
	m.selActive = BufferPos{Row: row, Col: col}
	m.markDirtyLocked()
}

// MouseUp 结束拖拽并保留该范围供 SelectedText 使用。
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

// ClearSelection 完全丢弃当前选择。
func (m *Model) ClearSelection() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.selAnchor = BufferPos{}
	m.selActive = BufferPos{}
	m.selecting = false
	m.markDirtyLocked()
}

// SelectionActive 报告当前是否正在进行拖拽。
func (m *Model) SelectionActive() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.selecting
}

// HasSelection 报告是否有非空范围可复制。
func (m *Model) HasSelection() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.hasSelectionLocked()
}

func (m *Model) hasSelectionLocked() bool { return m.selAnchor != m.selActive }

// selectionRangeLocked 按阅读顺序（左上优先）返回选择端点，
// 与拖拽方向无关。
func (m *Model) selectionRangeLocked() (BufferPos, BufferPos) {
	a, b := m.selAnchor, m.selActive
	if b.Row < a.Row || (b.Row == a.Row && b.Col < a.Col) {
		a, b = b, a
	}
	return a, b
}

// SelectedText 提取选择覆盖的单元格的纯文本。
// 宽 rune 的延续单元格被跳过，每行右修剪，使
// 结果便于粘贴。没有选择或
// 缓冲区为空时返回 ""。
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
	codeColor := m.palette.Code
	hasCodeColor := codeColor != ColorDefault
	lines := make([]string, 0, b.Row-a.Row+1)
	inCode := false
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
			cell := buf.cells[row*w+col]
			if cell.Text == "" { // 宽字符的续格没有文本，跳过
				continue
			}
			// 行内代码段在屏幕上丢了反引号，用内部标记圈出，稍后由
			// 由 joinSelectedRows 还原。inCode 跨行延续，支持被折行切开的代码段。
			isCode := hasCodeColor && cell.S.Fg == codeColor
			if isCode != inCode {
				line.WriteString(codeSpanMark)
				inCode = isCode
			}
			line.WriteString(cell.Text)
		}
		lines = append(lines, strings.TrimRight(line.String(), " "))
	}
	if inCode && len(lines) > 0 {
		lines[len(lines)-1] += codeSpanMark
	}
	return joinSelectedRows(lines, w)
}

// MouseWheel 把滚轮报告映射到现有的三行滚动行为。
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
