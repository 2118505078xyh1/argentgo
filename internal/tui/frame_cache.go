package tui

import "time"

// frameCacheKey 是 Paint 写入 Buffer 的单元格网格的所有输入的快照。
// 键相同的两帧保证绘制相同的单元格；键不同的两帧需要重新 Paint。
// 相同的单元格；键不同的两帧需要重新 Paint。
//
// 这只是一种*能力*：run 循环中尚无消费者使用它。它
// 刻意保守 —— 当屏幕上有时间敏感内容时，键
// 携带原始时钟 token，使任意两个时刻都不可能相等，从而禁止
// 复用而不是冒陈旧帧的风险。关于 draw 为何未接到此键，见变更说明
// 中的报告。
type frameCacheKey struct {
	width, height int
	version       int // content version: entries, editor, theme, sessions, plan, ...
	scroll        int
	themeEpoch    int
	noColor       bool

	// clockSecond 跟踪头部时钟（1 秒分辨率）。
	clockSecond int
	// timeSensitive 在有通知、spinner、实时推理/工具行或
	// 旧式状态可见时为 true。此时 nowNanos 固定到精确时刻，使
	// 该键永不与后续帧匹配。为 false 时，nowNanos 为零。
	timeSensitive bool
	nowNanos      int64

	usageIn, usageOut, usageCached int
	contextWindow                  int
	cost                           string

	selAnchor, selActive BufferPos
	pending              bool
	pendingName          string
	pendingArgs          string
	pendingDanger        string

	// 面板几何。面板*数据*变化会提升 version；这些标量字段
	// 涵盖只把模型标脏的光标/滚动/筛选移动。
	showTasks, showWorkspace, showPlan, showSessions bool
	tasksScroll, workspaceCursor, workspaceScroll    int
	previewScroll, planScroll                        int
	sessionsCursor, sessionsScroll                   int
	sessionFilter                                    string
	sessionFiltering                                 bool
	expanded                                         bool
}

// newFrameCacheKey 在其自己的锁下快照模型。它从不修改
// 模型：通知的存在用 Len 而非 Current 读取，后者会裁剪
// 过期通知（从而改变栈）作为副作用。一个
// 过期但尚未裁剪的通知只会让键更保守。
func newFrameCacheKey(m *Model, width, height int) frameCacheKey {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.now()
	k := frameCacheKey{
		width:            width,
		height:           height,
		version:          m.version,
		scroll:           m.scroll,
		themeEpoch:       m.themeEpoch,
		noColor:          m.noColor,
		clockSecond:      int(now.Sub(m.started).Seconds()),
		usageIn:          m.usageIn,
		usageOut:         m.usageOut,
		usageCached:      m.usageCached,
		contextWindow:    m.contextWindow,
		cost:             m.cost,
		selAnchor:        m.selAnchor,
		selActive:        m.selActive,
		showTasks:        m.showTasks,
		showWorkspace:    m.showWorkspace,
		showPlan:         m.showPlan,
		showSessions:     m.showSessions,
		tasksScroll:      m.tasksScroll,
		workspaceCursor:  m.workspaceCursor,
		workspaceScroll:  m.workspaceScroll,
		previewScroll:    m.previewScroll,
		planScroll:       m.planScroll,
		sessionsCursor:   m.sessionsCursor,
		sessionsScroll:   m.sessionsScroll,
		sessionFilter:    m.sessionFilter,
		sessionFiltering: m.sessionFiltering,
		expanded:         m.expanded,
	}
	if m.pending != nil {
		k.pending = true
		k.pendingName = m.pending.name
		k.pendingArgs = m.pending.args
		k.pendingDanger = m.pending.danger
	}

	// 旧式状态有 6 秒的显示窗口，取决于 now 而非
	// 任何版本。同样把存活的旧式状态视为时间敏感。
	legacyStatusLive := m.status != "" && now.Sub(m.statusAt) < 6*time.Second
	noticeLive := m.notices.Len() > 0
	liveEntry := false
	for i := range m.entries {
		e := &m.entries[i]
		if (e.Kind == EntryReasoning && m.working) || isLiveToolEntry(*e) {
			liveEntry = true
			break
		}
	}
	k.timeSensitive = m.working || legacyStatusLive || noticeLive || liveEntry
	if k.timeSensitive {
		k.nowNanos = now.UnixNano()
	}
	return k
}

// frameCacheHit 报告为 prev 渲染的帧能否复用于
// cur 而无需重绘。只有键相等才是安全的情况。
func frameCacheHit(prev, cur frameCacheKey) bool {
	return prev == cur
}

// isPureScrollChange 报告 cur 与 prev 是否仅在
// 记录滚动偏移上不同：内容版本和所有其他视觉输入
// 都未改变。这样的转换是缓存未命中 —— 滚动会移动正文
// 窗口和状态行的 "↑N" 标记，因此其字节必须重绘。
func isPureScrollChange(prev, cur frameCacheKey) bool {
	if prev == cur {
		return false
	}
	cur.scroll = prev.scroll
	return prev == cur
}
