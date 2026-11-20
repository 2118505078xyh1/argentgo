package tui

import (
	"time"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// EventKind names the agent events the full-screen UI consumes. The values
// match the agent package's EventToolStart / EventToolEnd so an integrator can
// convert one stream to another without a lookup table.
type EventKind string

// Event kinds understood by Model.HandleEvent.
const (
	EventToolStart EventKind = "tool_start"
	EventToolEnd   EventKind = "tool_end"
	EventAttempt   EventKind = "attempt"
	// EventError matches agent.EventError; Detail carries the redacted error
	// text so an integrator can copy agent.Event.Err straight across.
	EventError EventKind = "error"
	// EventRetry is TUI-only live backoff feedback (the agent stream has no
	// dedicated retry kind). The Retry* fields describe the pending attempt.
	EventRetry EventKind = "retry"
)

// Event is the frontend-facing subset of the agent event stream. tui keeps its
// own small value so it does not have to import internal/agent; the caller
// copies the fields it needs out of agent.Event.
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

	// EventRetry only: live backoff feedback for a failed request.
	RetryReason  string
	RetryAttempt int
	RetryMax     int
	RetryWaitMS  int64
}

// HandleEvent folds one agent event into the UI. Unknown kinds are ignored, so
// a caller may forward the whole stream without filtering it first.
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
		// Stream errors used to vanish here, leaving a "frozen screen". Reuse
		// the existing attempt-badge renderer instead of adding a paint path.
		m.appendNoticeEntryLocked(StreamErrorEntry(event.Detail, llm.RedactText))
	case EventRetry:
		text := RetryNotice(event.RetryReason, event.RetryAttempt, event.RetryMax, event.RetryWaitMS)
		m.appendNoticeEntryLocked(Entry{
			Kind: EntryNotice,
			Text: text,
		})
		// Retry is transient backoff feedback: mirror it onto the
		// severity-ranked status line so live progress is visible without
		// scanning the transcript. NoticeStack retires it on its own TTL, and
		// a fresh retry outranks older ones by severity then sequence.
		m.setStatusSeverityLocked(SeverityWarn, text)
	}
}

// appendNoticeEntryLocked appends a fully-built notice entry — already carrying
// its attempt badge, if any — and marks the model dirty so the next frame shows
// it. It is the shared tail for the stream-error and retry event mappings.
func (m *Model) appendNoticeEntryLocked(entry Entry) {
	m.flushStreamLocked()
	m.entries = append(m.entries, entry)
	m.version++
	m.markDirtyLocked()
}

func (m *Model) toolStartLocked(event Event) {
	started := m.now()
	// An approval modal may already have registered a pending task for this
	// tool. Promote the newest matching one instead of duplicating the row.
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
	// Drop the oldest finished calls first; an in-flight call must stay in the
	// list even when the history is full.
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
