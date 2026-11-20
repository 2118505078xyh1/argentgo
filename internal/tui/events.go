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
	Detail     string      // EventAttempt / EventError only: badge detail or error text

	// 仅 EventRetry：失败请求的实时退避反馈。
	RetryReason  string
	RetryAttempt int
	RetryMax     int
	RetryWaitMS  int64
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
