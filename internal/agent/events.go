package agent

import "github.com/2118505078xyh1/argent-go/internal/llm"

// EventKind names one observable step of a run.
type EventKind string

// The event vocabulary a frontend renders. Text and reasoning deltas arrive as
// the provider streams; tool events bracket one call; usage reports the delta
// for one request plus the run total.
const (
	EventTurnStart      EventKind = "turn_start"
	EventTurnEnd        EventKind = "turn_end"
	EventTextDelta      EventKind = "text_delta"
	EventReasoningDelta EventKind = "reasoning_delta"
	EventToolStart      EventKind = "tool_start"
	EventToolEnd        EventKind = "tool_end"
	EventUsage          EventKind = "usage"
	EventError          EventKind = "error"
	EventNotice         EventKind = "notice"
	EventStop           EventKind = "stop"
	EventSubagentStart  EventKind = "subagent_start"
	EventSubagentEnd    EventKind = "subagent_end"
)

// Event is one item on the run's event stream.
//
// Only the fields that make sense for Kind are populated, so a renderer can
// switch on Kind and read the rest without further checks:
//
//	turn_start / turn_end    Turn
//	text_delta               Turn, Text
//	reasoning_delta          Turn, Text (a whole reasoning block)
//	tool_start               Turn, ToolCallID, ToolName, Args
//	tool_end                 Turn, ToolCallID, ToolName, Output, IsError, DurationMS
//	usage                    Turn, Usage (this request), TotalUsage (run total)
//	error                    Err
//	notice                   Text
//	stop                     Reason
//	subagent_start           Text (task label), ToolName
//	subagent_end             Text (task label), Output, IsError, Err
type Event struct {
	Kind       EventKind
	Turn       int
	Depth      int // subagent nesting depth; 0 for the root run
	Text       string
	ToolCallID string
	ToolName   string
	Args       string
	Output     string
	IsError    bool
	DurationMS int64
	Usage      llm.Usage
	TotalUsage llm.Usage
	Err        string
	Reason     string
}
