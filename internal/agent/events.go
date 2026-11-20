package agent

import (
	"time"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// EventKind names one observable step of a run.
type EventKind string

// The event vocabulary a frontend renders. Text and reasoning deltas arrive as
// the provider streams; tool events bracket one call; usage reports the delta
// for one request plus the run total.
const (
	EventTurnStart       EventKind = "turn_start"
	EventTurnEnd         EventKind = "turn_end"
	EventCheckpointReady EventKind = "checkpoint_ready"
	EventTextDelta       EventKind = "text_delta"
	EventReasoningDelta  EventKind = "reasoning_delta"
	EventToolStart       EventKind = "tool_start"
	EventToolEnd         EventKind = "tool_end"
	EventUsage           EventKind = "usage"
	EventError           EventKind = "error"
	EventNotice          EventKind = "notice"
	EventStop            EventKind = "stop"
	EventSubagentStart   EventKind = "subagent_start"
	EventSubagentEnd     EventKind = "subagent_end"
	EventRetry           EventKind = "retry"
)

// RetryInfo is the wire-safe payload of an EventRetry. It is a closed set of
// small scalars: no response body, header or credential-shaped provider text
// is ever copied here. A frontend can draw "retry N of M" progress and a
// backoff countdown from it without parsing the model's error string.
type RetryInfo struct {
	Attempt     int    `json:"attempt"`
	MaxAttempts int    `json:"max_attempts,omitempty"`
	DelayMS     int64  `json:"delay_ms,omitempty"`
	Reason      string `json:"reason,omitempty"`
	Model       string `json:"model,omitempty"`
}

// RetryInfoFromAttempt maps a redacted llm retry decision onto EventRetry's
// wire payload. The llm layer owns the backoff, so delay is supplied by the
// caller (RetryPolicy.Delay); only the closed Reason token is copied from the
// decision, never Cause, which is free-form provider text.
//
// The app layer is the intended caller: it already installs llm's AttemptHook
// and builds both the protocol clients and the agent Runner's Emit, so it can
// bridge each AttemptRetry record here for per-backoff progress in addition to
// the model-switch retries the agent emits on its own.
func RetryInfoFromAttempt(a llm.RetryAttempt, model string, maxAttempts int, delay time.Duration) RetryInfo {
	return RetryInfo{
		Attempt:     a.Attempt,
		MaxAttempts: maxAttempts,
		DelayMS:     delay.Milliseconds(),
		Reason:      string(a.Reason),
		Model:       model,
	}
}

// Event is one item on the run's event stream.
//
// Only the fields that make sense for Kind are populated, so a renderer can
// switch on Kind and read the rest without further checks:
//
//	turn_start / turn_end    Turn
//	checkpoint_ready         Turn, Checkpoint (emitted before a mutating batch)
//	text_delta               Turn, Text
//	reasoning_delta          Turn, Text (a whole reasoning block)
//	tool_start               Turn, ToolCallID, ToolName, Args
//	tool_end                 Turn, ToolCallID, ToolName, Output, IsError, DurationMS
//	usage                    Turn, Usage (this request), TotalUsage (run total)
//	error                    Err, Failure (redacted classification; may be nil)
//	notice                   Text
//	stop                     Reason
//	subagent_start           Text (task label), ToolName
//	subagent_end             Text (task label), Output, IsError, Err
//	retry                    Turn, Retry (attempt, max, backoff delay, reason, model)
type Event struct {
	Kind       EventKind
	Turn       int
	Depth      int // subagent nesting depth; 0 for the root run
	Text       string
	ToolCallID string
	ToolName   string
	Args       string
	// Intent is the concise `i` string the model attached to a tool call. It
	// is populated on tool_start / tool_end so a verbose or malformed call can
	// be explained after the fact; it stays empty for every other event kind.
	Intent     string
	Output     string
	IsError    bool
	DurationMS int64
	Usage      llm.Usage
	TotalUsage llm.Usage
	Err        string
	// Failure is a redacted classification of Err, populated only on EventError.
	// It carries a fixed ErrorID and routing hints (Retryable / UserAction) so a
	// frontend can send a retryable failure back to the model or a settings
	// failure to the credentials/model screen without parsing the error text.
	// It stays nil for every other event kind and for producers that construct an
	// EventError directly, keeping their JSON byte-for-byte unchanged.
	Failure *llm.FailureInfo `json:"failure,omitempty"`
	Reason  string
	// PendingMessages is the number of steering messages currently queued. It
	// is populated on turn_start / turn_end / stop events so a frontend can
	// show an "N queued" hint without draining the steering channel; it stays
	// zero for every other event kind.
	PendingMessages int
	// Checkpoint is populated only for EventCheckpointReady. It carries the
	// information a session checkpoint needs before a mutating batch starts.
	Checkpoint *Checkpoint
	// Retry is populated only for EventRetry. It carries one retry decision in
	// wire-safe terms. It stays nil for every other event kind and for producers
	// that build an EventRetry directly, keeping their JSON byte-for-byte
	// unchanged.
	Retry *RetryInfo `json:"retry,omitempty"`
}

// IsTerminal reports whether e ends a run and is therefore must-deliver: a
// transport must not discard it even when a subscriber's buffer is full, or a
// headless client waiting for the completion signal would hang.
//
// EventStop is the single run terminal. EventTurnEnd is only a per-turn
// boundary (a run can emit many of them) and is deliberately not terminal.
func (e Event) IsTerminal() bool {
	return e.Kind == EventStop
}
