package session

import (
	"encoding/json"
	"fmt"
	"strings"
)

// EventKind 是 JSONL 记录 kind 的判别联合（tagged union，清单 #185）。
// 它把开放字符串收敛为可 switch 的强类型，同时保留 Unknown 以承接
// 带 ignorable 标记的未来事件——未知事件不会被静默当成已知类型。
type EventKind int

const (
	EventHeader EventKind = iota
	EventUser
	EventAssistant
	EventTool
	EventMessage
	EventSummary
	EventCompaction
	EventCheckpoint
	EventAttempt
	EventTitle
	EventSchema
	EventContextEdit
	EventImageOffload
	EventRequestSnapshot
	EventArchive
	EventToolClaim
	EventStats
	EventOther   // 已知但非对话的元数据（progress/usage/turn/...）
	EventUnknown // 未登记且 ignorable 的未来事件
)

// eventKindOf 把线协议上的字符串 kind 映射到判别联合。别名（prompt/model/reply/
// tool_result 等）收敛到同一变体，使 switch 不必重复处理历史拼写。
func eventKindOf(kind string) EventKind {
	switch kind {
	case KindHeader:
		return EventHeader
	case KindUser, "prompt":
		return EventUser
	case KindAssistant, "model", "reply":
		return EventAssistant
	case KindTool, "tool_result", "tool-result", "toolresult":
		return EventTool
	case KindMessage:
		return EventMessage
	case KindSummary:
		return EventSummary
	case KindCompaction:
		return EventCompaction
	case KindCheckpoint:
		return EventCheckpoint
	case KindAttempt:
		return EventAttempt
	case KindTitle:
		return EventTitle
	case KindSchema:
		return EventSchema
	case KindContextEdit:
		return EventContextEdit
	case KindImageOffload:
		return EventImageOffload
	case KindRequestSnapshot:
		return EventRequestSnapshot
	case "archive":
		return EventArchive
	case KindToolClaim:
		return EventToolClaim
	case "stats", "usage", "progress", "token", "cost", "budget", "entry",
		"notice", "error", "event", "trace", "snapshot", "system",
		"tool_progress", "turn":
		return EventOther
	default:
		return EventUnknown
	}
}

// Event 是统一解码边界的产物（清单 #189）：一个判别类型加原始记录。
// 调用方用 `switch e.Kind` 窄化，需要原始字节时读 e.Record——解码不丢字段，
// 因此旧会话逐字节兼容。
type Event struct {
	Kind   EventKind
	Record Record
	// correlationID 是事件族共享的稳定业务 id（#192）。优先取载荷里已有的
	// toolCallId/attemptId/id，否则回落到记录自己的 id；不写回转录。
	CorrelationID string
}

// DecodeRecord 是 JSONL 单行文本的唯一解码入口。
// 空行返回 ok=false；格式错误返回 error（调用方决定是否宽松跳过）。
// 未知 kind 不由本函数拒绝——fail-closed 由 CheckUnknownKinds 在读完整转录后
// 统一判定，这样单行解码保持纯粹、可被 doctor 与增量读取复用。
func DecodeRecord(line string) (Event, bool, error) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return Event{}, false, nil
	}
	var record Record
	if err := json.Unmarshal([]byte(trimmed), &record); err != nil {
		return Event{}, false, fmt.Errorf("session: decode record: %w", err)
	}
	return NewEvent(record), true, nil
}

// NewEvent 从一条记录构造强类型事件。纯函数，不修改 record。
func NewEvent(record Record) Event {
	return Event{
		Kind:          eventKindOf(record.Kind),
		Record:        record,
		CorrelationID: correlationIDOf(record),
	}
}

// correlationIDOf 从载荷或记录 id 派生稳定的事件族 id。
func correlationIDOf(record Record) string {
	for _, key := range []string{"toolCallId", "tool_call_id", "attemptId", "attempt_id", "correlationId"} {
		if value := fieldString(record.Data, key, ""); value != "" {
			return value
		}
	}
	return record.ID
}
