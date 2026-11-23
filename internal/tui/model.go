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
	// EntryStats 是一轮结束后的单行统计（模型 · 时长 · tok/s）。
	// 追加在末尾以保持既有种类的数值稳定。
	EntryStats
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

	// Detail 是错误类通知的完整（脱敏、限长）文本。它只在点击展开时
	// 渲染，短 Text 始终可独立阅读；普通记录为空，帧字节不变。
	Detail string
	// sticky 标记错误类通知不可被 TTL 退休。记录条目本身不随状态行过期，
	// 该标志让「错误免过期」的意图在模型里显式可查。
	sticky bool
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

// bodyHit 把一个记录条目的渲染行范围映射到屏幕坐标。top 含、
// bottom 不含；reasoningBodyHits 相对于记录顶部，reasoningHits 已由
// Paint 转成 0 基屏幕行，供 MouseDown 做命中测试。
type bodyHit struct {
	index       int // entries 下标（entries 只追加，下标稳定）
	top, bottom int
}

// completionHit 是补全面板中一条候选的可点击区域（屏幕行与列区间）。
type completionHit struct {
	index               int // visibleCommands 下标
	row, colFrom, colTo int
}

// TurnAction 是 undo / retry / rename 的窄接缝。TUI 从不
// 导入 session 或 app 包；由集成方提供行为。
type TurnAction func(ctx context.Context, arg string) error

// Config 为 Model 提供种子。
type Config struct {
	Root     string
	Model    string
	Thinking string // 当前思考强度，常驻头部状态栏
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
	kind     EntryKind
	text     string
	name     string
	args     string
	output   string
	running  bool
	failed   bool
	attempt  AttemptKind
	width    int
	expanded bool
	// reasoningExpanded 是单条思考块的展开态，加入缓存键确保点击
	// 展开/折叠后缓存行立即失效。
	reasoningExpanded bool
	noColor           bool
	themeEpoch        int
	locale            string
	// detail / errorExpanded 让错误通知的详情文本与展开态参与缓存失效，
	// 点击展开后旧缓存行不会继续显示折叠版。
	detail        string
	errorExpanded bool
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
	thinking string // 当前思考强度（如 high/xhigh），头部状态栏常驻显示
	root     string
	locale   string
	sessions []SessionInfo
	// vimMode/vimInsert 是可选的 vim 键位状态，零值 false 时输入行保持经典行为，
	// 逐字节不变。vimInsert 表示处于插入态（字母键正常输入），normal 态字母键
	// 走 VimMotion 而不插入字符。
	vimMode   bool
	vimInsert bool

	theme    string
	palette  Palette
	noColor  bool
	degraded bool
	env      func(string) string

	entries []Entry
	editor  Editor
	// kill 是输入行的 Emacs kill-ring。零值可用；与 vimMode 正交，
	// 默认开启（ARGENT_KILL_RING=0 时删除键仍照常工作，只是不记入环）。
	kill      killRing
	stream    strings.Builder
	streamBuf MarkdownStreamBuf // holds back incomplete markdown markers

	// reasoningAllExpanded 是"展开全部思考块"的全局开关，默认 false，
	// 使经典折叠摘要帧与 R33 逐字节一致。打开后所有未单独覆盖的
	// 思考块都走展开路径；逐块点击写入 reasoningExpanded 覆盖它。
	reasoningAllExpanded bool
	// reasoningExpanded 逐条记录思考块的展开状态，key 是 entries 下标。
	// entries 只在尾部追加（/clear 会整体清空），因此下标不会漂移。
	// 默认 nil 表示没有逐块覆盖，全部回落到 reasoningAllExpanded。
	reasoningExpanded map[int]bool
	// reasoningBodyHits / reasoningHits 是上一次 Paint 记录的思考块
	// 行范围，分别为记录相对坐标与屏幕坐标。它们不参与脏标记。
	reasoningBodyHits []bodyHit
	reasoningHits     []bodyHit

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
	// usageVisible 是头部/状态栏用量与成本行的渲染门控。默认 false，
	// 使未显式开启时 TUI 帧字节与接线前逐字节一致；只有 opt-in
	// （ARGENT_TUI_USAGE=1，由 app 入口的 SetUsageVisible 下沉）才显示。
	usageVisible bool
	// usageRate 是相邻两次 Usage 事件之间的输出速度，形如 "45 tok/s"。
	// 空字符串表示没有可计算的速率，因此用量行保持经典字节。
	// usageLastTokens/usageLastAt 是上一帧 Usage 的基线；两者在首个
	// 事件之前为零值，使首帧不会产生 ∞ 或 NaN。
	usageRate       string
	usageLastTokens int
	usageLastAt     time.Time
	// 可选流式计时（R36）：Usage 事件带首 token/流结束/完成时间点时，
	// 按轮记录三者，并用 (usageStreamedAt-usageFirstTokenAt) 作为纯流式
	// 时长跨步累加；速率取 Σtokens/Σdurations。字段缺失时以下全为零值，
	// usageRate 走上面的相邻 Usage 增量降级路径，显示字节与 R33 一致。
	usageFirstTokenAt   time.Time
	usageStreamedAt     time.Time
	usageCompletedAt    time.Time
	usageStreamTokens   int
	usageStreamDuration time.Duration
	// 回合统计的打点与结算状态。TurnStart 打点，TurnEnd 只置 pending，
	// 真正 append EntryStats 由随后的 Usage 事件触发。
	turnStartAt      time.Time
	turnOutTokens    int
	turnStatsPending bool
	expanded         bool

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
	// completionOverlay 是输入 `/` 后自动弹出的浮层开关，与 showCommands
	// 的侧栏语义完全分离：Ctrl+O 仍只切换侧栏，旧帧字节不变。
	completionOverlay bool
	// completionHits 是上一次 Paint 记录的浮层候选可点击区域。
	completionHits []completionHit

	// quitArmedAt 是空闲 Ctrl+C 第一次确认退出的时刻；零值表示尚未武装。
	// 3 秒内的第二次 Ctrl+C 才真正退出，避免误触直接结束会话。
	quitArmedAt time.Time
	// clearRequested 由非会话栏的 Ctrl+L 置位，run 循环消费后向终端写清屏序列。
	// TUI 自身不拥有终端，因此以请求标志而非直接写转义码的方式接线。
	clearRequested bool
	// errorExpanded 逐条记录错误通知详情的展开态，key 是 entries 下标。
	// nil 表示全部折叠。errorBodyHits / errorHits 与思考块同样按 Paint 记录。
	errorExpanded map[int]bool
	errorBodyHits []bodyHit
	errorHits     []bodyHit

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
		thinking: cfg.Thinking,
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

// SetUsageVisible 控制头部与状态栏是否显示用量/成本行。默认关闭，
// 使帧字节与 opt-in 门控接入前逐字节一致；开启后用量行才会渲染。
// 从任何 goroutine 调用都是安全的（由 Model.mu 保护）。
func (m *Model) SetUsageVisible(visible bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.usageVisible = visible
	m.markDirtyLocked()
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
// 先前的选择。命中的思考行会被点击展开/折叠，命中的补全候选会被
// 接受；只有两者都未命中时才进入经典文本选择路径。
func (m *Model) MouseDown(col, row int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if hit, ok := m.completionHitLocked(col, row); ok {
		m.completion.Select(hit.index)
		m.commandsCursor = hit.index
		m.markDirtyLocked()
		m.acceptCompletionLocked()
		return
	}
	if idx, ok := m.errorHitLocked(col, row); ok {
		// 点击错误通知切换详情展开，不影响文本选择。
		m.toggleErrorAt(idx)
		m.version++
		m.markDirtyLocked()
		return
	}
	if idx, ok := m.reasoningHitLocked(col, row); ok {
		// 逐块点击写入覆盖表，优先于全局开关。
		m.toggleReasoningAt(idx)
		m.version++
		m.markDirtyLocked()
		return
	}
	m.selAnchor = BufferPos{Row: row, Col: col}
	m.selActive = m.selAnchor
	m.selecting = true
	m.markDirtyLocked()
}

// reasoningHitLocked 报告屏幕位置落在哪个思考块的行范围内。
// 行范围来自上一次 Paint；条目已被清空或不再是思考块时视为未命中。
func (m *Model) reasoningHitLocked(col, row int) (int, bool) {
	if col < 0 || row < 0 {
		return 0, false
	}
	for _, hit := range m.reasoningHits {
		if row < hit.top || row >= hit.bottom {
			continue
		}
		if hit.index < 0 || hit.index >= len(m.entries) || m.entries[hit.index].Kind != EntryReasoning {
			continue
		}
		return hit.index, true
	}
	return 0, false
}

// reasoningExpandedAt 返回下标 idx 的思考块是否应展开。逐块覆盖优先；
// 没有覆盖时回落到全局开关。默认（全局关、无覆盖）恒为 false，
// 因此折叠帧与 R33 逐字节一致。nil map 读取是安全的。
func (m *Model) reasoningExpandedAt(idx int) bool {
	if expanded, ok := m.reasoningExpanded[idx]; ok {
		return expanded
	}
	return m.reasoningAllExpanded
}

// toggleReasoningAt 翻转单个思考块，并把结果写成逐块覆盖，
// 使其优先于全局开关。调用方持有 mu（或处于单线程测试）。
func (m *Model) toggleReasoningAt(idx int) {
	if m.reasoningExpanded == nil {
		m.reasoningExpanded = make(map[int]bool)
	}
	m.reasoningExpanded[idx] = !m.reasoningExpandedAt(idx)
}

// toggleReasoningAllLocked 翻转全局开关并清空逐块覆盖，使"切换全部"
// 真正作用于每一个思考块。清空后逐块点击可再次建立覆盖。
// 调用方持有 mu。
func (m *Model) toggleReasoningAllLocked() {
	m.reasoningAllExpanded = !m.reasoningAllExpanded
	m.reasoningExpanded = nil
	m.version++
	m.markDirtyLocked()
	if m.reasoningAllExpanded {
		m.setStatusLocked(text(m.locale, "thinking_all_on"))
	} else {
		m.setStatusLocked(text(m.locale, "thinking_all_off"))
	}
}

// errorExpandedAt 返回下标 idx 的错误通知详情是否展开。nil map 读取安全，
// 默认恒为折叠，因此无错误详情时帧字节不变。
func (m *Model) errorExpandedAt(idx int) bool {
	return m.errorExpanded[idx]
}

// toggleErrorAt 翻转单条错误通知的详情展开态。调用方持有 mu。
func (m *Model) toggleErrorAt(idx int) {
	if m.errorExpanded == nil {
		m.errorExpanded = make(map[int]bool)
	}
	m.errorExpanded[idx] = !m.errorExpandedAt(idx)
}

// errorHitLocked 报告屏幕位置落在哪条错误通知上。行范围来自上一次 Paint。
func (m *Model) errorHitLocked(col, row int) (int, bool) {
	if col < 0 || row < 0 {
		return 0, false
	}
	for _, hit := range m.errorHits {
		if row < hit.top || row >= hit.bottom {
			continue
		}
		if hit.index < 0 || hit.index >= len(m.entries) || m.entries[hit.index].Detail == "" {
			continue
		}
		return hit.index, true
	}
	return 0, false
}

// consumeClearRequest 取出并清空 Ctrl+L 的清屏请求。run 循环消费它后
// 向终端写清屏序列并强制整屏重绘。
func (m *Model) consumeClearRequest() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.clearRequested {
		return false
	}
	m.clearRequested = false
	return true
}

// completionHitLocked 报告屏幕位置是否落在补全浮层的某条候选上。
func (m *Model) completionHitLocked(col, row int) (completionHit, bool) {
	if !m.completionOverlay || row < 0 {
		return completionHit{}, false
	}
	for _, hit := range m.completionHits {
		if row == hit.row && col >= hit.colFrom && col <= hit.colTo {
			return hit, true
		}
	}
	return completionHit{}, false
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
