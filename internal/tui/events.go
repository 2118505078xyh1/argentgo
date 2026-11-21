package tui

import (
	"time"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// EventKind 命名全屏 UI 消费的 agent 事件。这些值
// 与 agent 包的 EventToolStart / EventToolEnd 一致，使集成方
// 无需查找表即可把一种流转换为另一种。
type EventKind string

// Model.HandleEvent 理解的事件种类。
const (
	EventToolStart EventKind = "tool_start"
	EventToolEnd   EventKind = "tool_end"
	EventAttempt   EventKind = "attempt"
	// EventError 对应 agent.EventError；Detail 携带清洗后的错误
	// 文本，使集成方可以直接复制 agent.Event.Err。
	EventError EventKind = "error"
	// EventRetry 是 TUI 独有的实时退避反馈（agent 流没有
	// 专门的重试类别）。Retry* 字段描述待处理的尝试。
	EventRetry EventKind = "retry"
	// EventToolProgress 是长工具执行期间的阶段进度。Detail 携带
	// 原始进度文本，HandleEvent 用 parseToolProgress 提取可读标签后
	// 显示为通知。它对应 agent 的 tool_progress 通知，但刻意使用独立
	// 种类，避免影响普通 EventNotice 的处理。
	EventToolProgress EventKind = "tool_progress"
	// TuiEventUsage 对应 agent 的 usage 事件，携带本轮提示/补全/缓存
	// token 计数。追加在枚举末尾以保持既有种类数值稳定；没有该事件时
	// 模型不调用 SetUsage/SetCachedTokens，用量恒为零、帧字节不变。
	TuiEventUsage EventKind = "usage"
	// TuiEventTurnStart / TuiEventTurnEnd 标记一个 agent 回合的边界。
	// app 侧发送同名字符串事件；两者都不直接产生记录，只有随后到达的
	// Usage 才结算统计行（agent 的顺序是 TurnEnd → Usage）。
	TuiEventTurnStart EventKind = "turn_start"
	TuiEventTurnEnd   EventKind = "turn_end"
)

// Event 是 agent 事件流面向前端的子集。tui 保留自己的
// 小值对象，这样就不必导入 internal/agent；调用方
// 从 agent.Event 中复制所需字段。
type Event struct {
	Kind       EventKind
	ToolCallID string
	ToolName   string
	Args       string
	Output     string
	IsError    bool
	DurationMS int64
	Attempt    AttemptKind // EventAttempt only
	Detail     string      // EventAttempt / EventError / EventToolProgress only: badge detail, error text or raw progress text

	// 仅 EventRetry：失败请求的实时退避反馈。
	RetryReason  string
	RetryAttempt int
	RetryMax     int
	RetryWaitMS  int64

	// 仅 TuiEventUsage：token 用量计数。旧事件保持零值，
	// 因此渲染门（usageIn+usageOut>0、usageCached>0）不会误触发。
	PromptTokens     int
	CompletionTokens int
	CachedTokens     int
}

// HandleEvent 把一个 agent 事件折叠进 UI。未知类别被忽略，
// 因此调用方可以直接转发整个流而无需先过滤。
func (m *Model) HandleEvent(event Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch event.Kind {
	case EventToolStart:
		m.toolStartLocked(event)
	case EventToolEnd:
		m.toolEndLocked(event)
	case EventAttempt:
		if event.Attempt != "" {
			m.appendAttemptLocked(event.Attempt, event.Detail)
		}
	case EventError:
		// 流错误过去会在这里消失，留下"冻结的画面"。复用
		// 现有的尝试徽章渲染器，而不是新增绘制路径。
		m.appendNoticeEntryLocked(StreamErrorEntry(event.Detail, llm.RedactText))
	case EventRetry:
		text := RetryNotice(event.RetryReason, event.RetryAttempt, event.RetryMax, event.RetryWaitMS)
		m.appendNoticeEntryLocked(Entry{
			Kind: EntryNotice,
			Text: text,
		})
		// Retry 是瞬时的退避反馈：把它镜像到
		// 按严重度排序的状态行，使实时进度可见而无需
		// 扫描记录。NoticeStack 按其 TTL 自行退休它，且
		// 新的重试按严重度再按序号优先于旧的。
		m.setStatusSeverityLocked(SeverityWarn, text)
	case EventToolProgress:
		// 只有能解析出阶段标签的进度才显示；无法识别的负载一律忽略，
		// 与未知事件保持一致的副作用。沿用既有通知渲染路径，不新增
		// 绘制分支，因此不改变任何现有事件的字节级输出。
		if label, ok := parseToolProgress(event.Detail); ok {
			m.appendNoticeEntryLocked(Entry{Kind: EntryNotice, Text: label})
		}
	case TuiEventUsage:
		// 用量事件只更新头部/状态行的计数（复用现有 SetUsage 与
		// SetCachedTokens 的持锁实现），不追加记录、不新增绘制分支。
		// HandleEvent 已持有 m.mu，必须调用 *Locked 变体，否则会对
		// 不可重入的 sync.Mutex 再次加锁而死锁。
		m.setUsageLocked(event.PromptTokens, event.CompletionTokens)
		m.setCachedTokensLocked(event.CachedTokens)
		// 增量速度：相邻两次 Usage 的补全 token 差除以墙钟差。
		// 首次事件没有基线、Δt<=0 或 Δtokens<=0 都不产生速率，
		// 因此绝不会出现 NaN/Inf，也保持无用量时的帧字节不变。
		now := m.now()
		if !m.usageLastAt.IsZero() && now.After(m.usageLastAt) {
			delta := event.CompletionTokens - m.usageLastTokens
			if delta > 0 {
				rate := float64(delta) / now.Sub(m.usageLastAt).Seconds()
				if rate > maxTokenRate {
					rate = maxTokenRate
				}
				m.usageRate = formatTokenRate(rate)
			}
		}
		m.usageLastTokens, m.usageLastAt = event.CompletionTokens, now
		// 统计行在 TurnStart 打点后累计输出 token；只有收到 Usage
		// 才结算（agent 先发 TurnEnd 再发 Usage）。
		m.turnOutTokens += event.CompletionTokens
		if m.turnStatsPending {
			m.finalizeTurnStatsLocked()
		}
	case TuiEventTurnStart:
		// 打点并清零上一轮的残留。仅打点不改变任何渲染字节。
		m.turnStartAt = m.now()
		m.turnOutTokens = 0
		m.turnStatsPending = false
	case TuiEventTurnEnd:
		// 只标记待结算，绝不在此处 append 记录：Usage 尚未到达，
		// 此刻的输出 token 数还不知道。
		if !m.turnStartAt.IsZero() {
			m.turnStatsPending = true
		}
	}
}

// appendNoticeEntryLocked 追加一条完整构建的通知条目 —— 若有的话已
// 携带其尝试徽章 —— 并标记模型为脏，使下一帧显示
// 它。它是流错误和重试事件映射的共用尾部。
func (m *Model) appendNoticeEntryLocked(entry Entry) {
	m.flushStreamLocked()
	m.entries = append(m.entries, entry)
	m.version++
	m.markDirtyLocked()
}

func (m *Model) toolStartLocked(event Event) {
	started := m.now()
	// 审批弹窗可能已经为该工具注册了一个待处理任务。
	// 提升最新匹配的那条，而不是重复该行。
	for i := len(m.tasks) - 1; i >= 0; i-- {
		task := &m.tasks[i]
		if task.Status == TaskPending && task.Name == event.ToolName {
			task.ID = event.ToolCallID
			task.Args = event.Args
			task.Status = TaskRunning
			task.Started = started
			m.version++
			m.trimTasksLocked()
			return
		}
	}
	m.appendTaskLocked(Task{
		ID:      event.ToolCallID,
		Name:    event.ToolName,
		Args:    event.Args,
		Status:  TaskRunning,
		Started: started,
	})
}

func (m *Model) toolEndLocked(event Event) {
	status := TaskOK
	if event.IsError {
		status = TaskError
	}
	duration := time.Duration(event.DurationMS) * time.Millisecond
	if event.IsError {
		for j := len(m.entries) - 1; j >= 0; j-- {
			entry := &m.entries[j]
			if entry.Kind == EntryTool && entry.Name == event.ToolName {
				entry.Failed, entry.Attempt = true, AttemptFailed
				break
			}
		}
	}
	for i := len(m.tasks) - 1; i >= 0; i-- {
		task := &m.tasks[i]
		match := event.ToolCallID != "" && task.ID == event.ToolCallID
		if event.ToolCallID == "" {
			match = task.Name == event.ToolName && task.Status == TaskRunning
		}
		if match {
			task.ID = event.ToolCallID
			task.Name = event.ToolName
			task.Args = event.Args
			task.Status = status
			task.Duration = duration
			m.version++
			m.capturePlanLocked(event)
			m.trimTasksLocked()
			return
		}
	}
	m.appendTaskLocked(Task{
		ID:       event.ToolCallID,
		Name:     event.ToolName,
		Args:     event.Args,
		Status:   status,
		Duration: duration,
	})
	m.capturePlanLocked(event)
}

func (m *Model) appendTaskLocked(task Task) {
	m.tasks = append(m.tasks, task)
	m.version++
	m.trimTasksLocked()
}

func (m *Model) trimTasksLocked() {
	if len(m.tasks) <= maxTasks {
		m.tasksScroll = clampScroll(m.tasksScroll, len(m.tasks))
		return
	}
	// 先丢弃最旧的已结束调用；进行中的调用必须留在
	// 列表中，即使历史已满。
	excess := len(m.tasks) - maxTasks
	drop := make([]bool, len(m.tasks))
	for i := 0; i < len(m.tasks) && excess > 0; i++ {
		status := m.tasks[i].Status
		if status == TaskPending || status == TaskRunning {
			continue
		}
		drop[i], excess = true, excess-1
	}
	for i := 0; i < len(m.tasks) && excess > 0; i++ {
		if !drop[i] {
			drop[i], excess = true, excess-1
		}
	}
	kept := m.tasks[:0]
	for i, task := range m.tasks {
		if !drop[i] {
			kept = append(kept, task)
		}
	}
	m.tasks = kept
	m.tasksScroll = clampScroll(m.tasksScroll, len(m.tasks))
}
