package tui

import (
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"
)

// 工具输出在进入模型时就被限制：单次读取生成的
// 文件不得把 100 MB 钉在内存中直到进程退出。
// 头+尾窗口同时保留有趣的开头和最后几行（错误
// 通常在那里）。截断感知字节和行，且绝不在
// UTF-8 rune 中间切开。
const (
	maxToolOutputBytes = 64 << 10
	maxToolOutputLines = 2000

	// markerReserve 为 "[…N 字节省略…]" 标记预留空间，使存储的
	// 字符串即使追加提示后仍在 maxToolOutputBytes 之内。
	markerReserve = 64
)

// SetPromptStore 给输入编辑器装上可选的持久化提示历史。
// 传 nil（默认）时编辑器不接触文件系统，行为与接线之前逐字节一致。
// 非 nil 时容错加载磁盘内容，并把最近 200 条按旧→新灌入内存环，
// 使重启后的第一次 Up 就能召回旧输入。加载/恢复的失败被吞掉：
// 历史只是便利功能，绝不能因为一个坏文件阻止 TUI 启动。
// 从任何 goroutine 调用都安全（由 Model.mu 保护）。
func (m *Model) SetPromptStore(s *PromptStore) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.editor.store = s
	if s == nil {
		return
	}
	_ = s.Load()
	m.editor.SeedHistory(s.Recent(200))
}

// AppendAttempt 向记录添加未提交的尝试徽章。
func (m *Model) AppendAttempt(kind AttemptKind, detail string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.appendAttemptLocked(kind, detail)
}

func (m *Model) appendAttemptLocked(kind AttemptKind, detail string) {
	m.flushStreamLocked()
	m.entries = append(m.entries, Entry{Kind: EntryNotice, Attempt: kind, Text: detail})
	m.version++
	m.markDirtyLocked()
}

// SetStatus 在状态行显示一条短暂的信息消息。
func (m *Model) SetStatus(text string) {
	m.mu.Lock()
	m.setStatusLocked(text)
	m.mu.Unlock()
}

// SetStatusSeverity 显示一条带显式严重度的短暂消息。
// 高严重度通知在存活期间优先于信息性通知。
func (m *Model) SetStatusSeverity(sev Severity, text string) {
	m.mu.Lock()
	m.setStatusSeverityLocked(sev, text)
	m.mu.Unlock()
}

// Scroll 把记录向后（正）或向底部（负）滚动。
func (m *Model) Scroll(delta int) {
	m.mu.Lock()
	m.scrollByLocked(delta)
	m.mu.Unlock()
}

// transcriptMetricsLocked 返回纯滚动算术所需的输入。
// Paint 每帧缓存已渲染的行数和视口高度；在
// 首次绘制之前（或刚追加条目之后）我们重新推导一个计数，
// 使按键仍能滚动，而不会被钳制到陈旧的零。
func (m *Model) transcriptMetricsLocked() (total, viewport int) {
	total, viewport = m.bodyLines, m.bodyH
	if viewport <= 0 {
		viewport = 10
	}
	if total <= 0 || m.version != m.bodyVersion {
		total = 0
		if len(m.entries) > 0 {
			width := m.bodyW
			if width <= 0 {
				width = 80
			}
			total = len(m.renderBodyLocked(width))
		}
	}
	return total, viewport
}

func (m *Model) scrollByLocked(delta int) {
	m.markDirtyLocked()
	total, viewport := m.transcriptMetricsLocked()
	m.scroll = scrollTranscript(m.scroll, delta, total, viewport)
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
	if m.showCommands {
		count := 0
		if m.completion != nil {
			count = m.completion.Count()
		}
		m.commandsScroll = clampScroll(m.commandsScroll+delta, maxInt(count-1, 0))
	}
}

// clampCommandsScrollLocked 让命令光标始终落在滚动视口内，
// 与会话栏的 clampSessionScrollLocked 行为一致。
func (m *Model) clampCommandsScrollLocked() {
	visible := maxInt(m.bodyH/4-1, 1)
	if m.commandsCursor < m.commandsScroll {
		m.commandsScroll = m.commandsCursor
	}
	if m.commandsCursor >= m.commandsScroll+visible {
		m.commandsScroll = m.commandsCursor - visible + 1
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
	m.setStatusSeverityLocked(SeverityInfo, text)
}

// setStatusSeverityLocked 发布一条状态通知。空文本退休所有
// 存活的通知，保留旧 setStatusLocked("") "清空状态行"
// 在提交时和动作成功后使用的行为。
func (m *Model) setStatusSeverityLocked(sev Severity, text string) {
	if text == "" {
		clearNoticeStack(&m.notices)
		m.status, m.statusAt = "", time.Time{}
		m.markDirtyLocked()
		return
	}
	now := m.now()
	switch sev {
	case SeverityInfo:
		m.notices.ReportInfo(text, DefaultNoticeTTL, now)
	case SeverityWarn:
		m.notices.ReportWarn(text, DefaultNoticeTTL, now)
	case SeverityError:
		m.notices.ReportError(text, DefaultNoticeTTL, now)
	default:
		m.notices.Push(sev, text, DefaultNoticeTTL, now)
	}
	m.markDirtyLocked()
}

// clearNoticeStack 退休所有存活通知而不重建栈，因此
// 其中的互斥锁从不被复制，序号保持唯一。
func clearNoticeStack(s *NoticeStack) {
	s.mu.Lock()
	s.notices = nil
	s.mu.Unlock()
}

// PruneNotices 退休 TTL 已过的通知。run 循环每次 tick
// 调用它，使过期不依赖状态行的重绘。
func (m *Model) PruneNotices(now time.Time) {
	m.mu.Lock()
	if m.notices.Prune(now) > 0 {
		m.markDirtyLocked()
	}
	m.mu.Unlock()
}

// AppendUser 添加一个用户块。
func (m *Model) AppendUser(text string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.appendUserLocked(text)
}

func (m *Model) appendUserLocked(text string) {
	m.flushStreamLocked()
	m.entries = append(m.entries, Entry{Kind: EntryUser, Text: text})
	m.version++
	m.markDirtyLocked()
	m.scroll = 0
}

func attemptFromFailed(failed bool) AttemptKind {
	if failed {
		return AttemptFailed
	}
	return ""
}

// AppendReasoning 添加（或扩展）一个推理块。
func (m *Model) AppendReasoning(text string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.appendReasoningLocked(text)
}

// appendReasoningLocked 是 AppendReasoning 的持锁内核，供已持有 m.mu 的
// 调用方（如 HandleEvent）直接调用，避免自锁死锁。调用方必须持有 m.mu。
func (m *Model) appendReasoningLocked(text string) {
	m.flushStreamLocked()
	if n := len(m.entries); n > 0 && m.entries[n-1].Kind == EntryReasoning {
		m.entries[n-1].Text += text
	} else {
		m.entries = append(m.entries, Entry{Kind: EntryReasoning, Text: text})
	}
	m.version++
	m.markDirtyLocked()
}

// AppendTool 添加一个已完成或运行中的工具调用。输出在这里、在入口处
// 受限，使单个条目绝无法在内存中钉住无界字符串。
func (m *Model) AppendTool(name, args, output string, failed bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.flushStreamLocked()
	m.entries = append(m.entries, Entry{
		Kind:    EntryTool,
		Name:    name,
		Args:    args,
		Output:  truncateToolOutput(output),
		Failed:  failed,
		Attempt: attemptFromFailed(failed),
	})
	m.version++
	m.markDirtyLocked()
	m.scroll = 0
}

// truncateToolOutput 保留输出的头尾窗口，丢弃
// 中间。同时强制字节和行预算，且每次切割都落在
// UTF-8 rune 边界，使 CJK 文本和 emoji 永不损坏。
func truncateToolOutput(output string) string {
	if len(output) <= maxToolOutputBytes && strings.Count(output, "\n") < maxToolOutputLines {
		return output
	}
	budget := maxToolOutputBytes - markerReserve
	headBytes, tailBytes := budget/2, budget-budget/2
	headLines, tailLines := maxToolOutputLines/2, maxToolOutputLines-maxToolOutputLines/2

	// 头窗口：至多 headBytes 字节和 headLines 行。
	headEnd := runeFloor(output, headBytes)
	if cut := prefixLineEnd(output[:headEnd], headLines); cut < headEnd {
		headEnd = cut
	}
	// 尾窗口：至多 tailBytes 字节和 tailLines 行。
	tailStart := runeCeil(output, len(output)-tailBytes)
	if cut := suffixLineStart(output[tailStart:], tailLines); cut > 0 {
		tailStart += cut
	}
	if headEnd > tailStart {
		mid := runeFloor(output, len(output)/2)
		headEnd, tailStart = mid, runeCeil(output, mid)
	}

	head, tail := output[:headEnd], output[tailStart:]
	omitted := len(output) - len(head) - len(tail)
	marker := fmt.Sprintf("[…%d 字节省略…]", omitted)
	return head + marker + tail
}

// runeFloor 返回 <= limit 且开启一个 UTF-8 rune 的最大 i。
func runeFloor(s string, limit int) int {
	if limit >= len(s) {
		return len(s)
	}
	if limit <= 0 {
		return 0
	}
	i := limit
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return i
}

// runeCeil 返回 >= limit 且开启一个 UTF-8 rune 的最小 i。
func runeCeil(s string, limit int) int {
	if limit <= 0 {
		return 0
	}
	if limit >= len(s) {
		return len(s)
	}
	i := limit
	for i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return i
}

// prefixLineEnd 返回紧接第 n 个换行之后的字节索引，或 len(s)。
func prefixLineEnd(s string, n int) int {
	count := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			count++
			if count == n {
				return i + 1
			}
		}
	}
	return len(s)
}

// suffixLineStart 返回一个 s 中的偏移，使后缀最多覆盖 n 行。
// 换行是 ASCII，因此结果始终是 rune 边界。
func suffixLineStart(s string, n int) int {
	if n <= 0 {
		return len(s)
	}
	count := 0
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '\n' {
			count++
			if count == n {
				return i + 1
			}
		}
	}
	return 0
}

// AppendNotice 添加一条系统通知。
func (m *Model) AppendNotice(text string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.appendNoticeLocked(text)
}

func (m *Model) appendNoticeLocked(text string) {
	m.flushStreamLocked()
	m.entries = append(m.entries, Entry{Kind: EntryNotice, Text: text})
	m.version++
	m.markDirtyLocked()
}

// AppendStream 在助手文本流入时追加。它不提升
// 渲染版本：文本每帧被折叠进条目一次，因此一千个增量的回合
// 每次重绘只复制一次，而不是每个增量复制一次。
// 可能是未完成 markdown 标记开头的文本会被
// streamBuf 暂时保留，直到该结构完成（或回合结束），因此半个
// `**` 或 ``` 围栏绝不会闪现在已渲染的记录中。
func (m *Model) AppendStream(delta string) {
	if delta == "" {
		return
	}
	m.mu.Lock()
	m.stream.WriteString(m.streamBuf.Push(delta))
	m.markDirtyLocked()
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
	m.markDirtyLocked()
}

// maxTokenRate 限制显示的速度上界，防止极短 elapsed 产生天文数字。
const maxTokenRate = 1e6

// formatTokenRate 把每秒 token 数格式化为一行文本。非有限值返回空
// 字符串，使异常输入永远不能把 "NaN"/"+Inf" 泄漏到统计行或用量行。
func formatTokenRate(rate float64) string {
	if math.IsNaN(rate) || math.IsInf(rate, 0) || rate < 0 {
		return ""
	}
	return fmt.Sprintf("%.0f tok/s", rate)
}

// finalizeTurnStatsLocked 结算一轮的统计行。调用方已持有 m.mu。
// 守卫覆盖三种缺失信息：没有 TurnStart 打点、时钟没有前进、没有输出
// token 或模型名为空；任一成立都不产生记录，因此异常回合不会留下
// 一行 NaN 或半截信息。结算后清掉打点，重复 Usage 不会重复追加。
func (m *Model) finalizeTurnStatsLocked() {
	elapsed := m.now().Sub(m.turnStartAt)
	if elapsed <= 0 || m.turnOutTokens <= 0 || m.model == "" {
		return
	}
	rate := math.Min(float64(m.turnOutTokens)/elapsed.Seconds(), maxTokenRate)
	line := m.model + " · " + formatDuration(elapsed) + " · " + formatTokenRate(rate)
	m.flushStreamLocked()
	m.entries = append(m.entries, Entry{Kind: EntryStats, Text: line})
	m.version++
	m.markDirtyLocked()
	m.turnStartAt = time.Time{}
	m.turnStatsPending = false
	m.turnOutTokens = 0
}

// EndTurn 结束正在进行的回合。非 nil 错误变成一条通知。
func (m *Model) EndTurn(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// 释放回合结束时仍被保留的任何标记前缀，使
	// 没有流式字节丢失；flushStreamLocked 把它折叠进存活的条目。
	m.stream.WriteString(m.streamBuf.Flush())
	m.flushStreamLocked()
	if n := len(m.entries); n > 0 {
		m.entries[n-1].live = false
	}
	m.working = false
	if err != nil {
		// 错误通知带完整详情与 /retry 动作提示，且标记为 sticky 免过期。
		m.appendNoticeEntryLocked(errorNoticeEntry(m.locale, err))
	}
	m.version++
	m.markDirtyLocked()
}
