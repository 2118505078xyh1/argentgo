package gui

// One tab's worker. The Tauri build shared argent_engine channels with the
// frontend; here the same three channels are the only contract, so the bridge
// never imports the agent loop.
//
// Every channel is owned by the caller of the factory and stays valid for the
// life of the tab. Events must be closed when the worker exits: the pump stops
// on that signal. Approvals must be buffered with room for one value, because
// answer_approval never blocks the HTTP handler.
type Runner interface {
	Events() <-chan Event
	Approvals() <-chan Approval
	Commands() chan<- Command
}

// SessionStore lists, searches and imports persisted sessions. internal/session
// satisfies this shape; the bridge only needs these three operations.
type SessionStore interface {
	List() ([]SessionRow, error)
	Search(query string, limit int) ([]SearchHit, error)
	Import(text, title string) (string, error)
}

// CommandKind names one request from the page to its worker.
type CommandKind string

// The command vocabulary, one-to-one with argent_ui_core::Command.
const (
	CommandPrompt        CommandKind = "prompt"
	CommandAbort         CommandKind = "abort"
	CommandSteer         CommandKind = "steer"
	CommandPause         CommandKind = "pause"
	CommandResume        CommandKind = "resume"
	CommandNewSession    CommandKind = "new_session"
	CommandOpenSession   CommandKind = "open_session"
	CommandRenameSession CommandKind = "rename_session"
	CommandDeleteSession CommandKind = "delete_session"
	CommandCompact       CommandKind = "compact"
	CommandRewind        CommandKind = "rewind"
	CommandSetModel      CommandKind = "set_model"
	CommandSetThinking   CommandKind = "set_thinking"
	CommandShutdown      CommandKind = "shutdown"
)

// Command carries one request to a worker. Only the fields that make sense for
// Kind are populated.
type Command struct {
	Kind   CommandKind
	Text   string // prompt / steer text
	Path   string // session file for open, rename and delete
	Title  string // new title for rename
	Model  string // set_model
	Level  string // set_thinking
	Steps  int    // rewind
	Paused bool   // pause / resume
}

// Approval is one tool call waiting for a human decision. The bridge assigns
// the id the page answers with, so ids stay unique across tabs.
type Approval struct {
	Name      string
	Arguments string
	// Reply receives the decision exactly once. Buffered, capacity >= 1.
	Reply chan<- bool
}

// EventKind names one frame the page understands (see ui/src/ipc.ts).
type EventKind string

// Event kinds a worker may emit. "approval" is produced by the bridge itself
// from Approval values, never by a Runner.
const (
	EventReady          EventKind = "ready"
	EventAgent          EventKind = "agent"
	EventTurnDone       EventKind = "turn_done"
	EventError          EventKind = "error"
	EventSession        EventKind = "session"
	EventSessionCleared EventKind = "session_cleared"
	EventLoaded         EventKind = "loaded"
	EventSessions       EventKind = "sessions"
)

// AgentEvent.Type values.
const (
	AgentTurnStart     = "turn_start"
	AgentTurnEnd       = "turn_end"
	AgentText          = "assistant_text"
	AgentThinking      = "thinking"
	AgentToolRequested = "tool_requested"
	AgentToolFinished  = "tool_finished"
	AgentFinished      = "finished"
	AgentNotice        = "notice"
	AgentAborted       = "aborted"
	AgentAttempt       = "attempt"
)

// Event is one item on a worker's output stream. Kind selects which field
// carries the payload.
type Event struct {
	Kind     EventKind
	Ready    *Ready
	Agent    *AgentEvent
	TurnDone *TurnDone
	Message  string // error
	Path     string // session file
	Entries  []LoadedEntry
	Sessions []SessionRow
}

// Ready is the per-tab capability snapshot: model list, thinking levels, cwd.
type Ready struct {
	Model      string
	Level      string
	ContextMax int
	Models     []ModelInfo
	Thinking   []string
	Cwd        string
	Provider   string
	Protocol   string
	Theme      string
}

// ModelInfo is one entry of the model picker.
type ModelInfo struct {
	ID            string
	ContextLength int
}

// AgentEvent mirrors the streamed step vocabulary of the agent loop.
type AgentEvent struct {
	Type       string
	Text       string
	Index      int
	ID         string
	Name       string
	Arguments  string
	Output     string
	IsError    bool
	DurationMS int64
	Reason     string
	Attempt    string // AgentAttempt: failed | retried | cancelled | stream_error
}

// TurnDone closes one turn and carries its usage summary.
type TurnDone struct {
	Reason           string
	Requests         int
	Cost             *float64
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	CachedTokens     int
}

// LoadedEntry is one replayed transcript row.
type LoadedEntry struct {
	Kind       string // user | assistant | thinking | notice | tool
	Level      string // notice level
	Text       string
	CallID     string
	Name       string
	Arguments  string
	Output     string
	State      string // tool state: running | ok | error
	DurationMS *int64
	Attempt    string // attempt badge for non-committed rows
}

// SessionRow is one row of the sidebar session list.
type SessionRow struct {
	Path    string
	Title   string
	Updated string
	Cwd     string
}

// SearchHit is one cross-session search result.
type SearchHit struct {
	Path    string
	Title   string
	Line    int
	Snippet string
}

// payload renders the event exactly like the Tauri backend's map_event, minus
// the sid the bridge adds. The second result is false for kinds the page cannot
// decode, which are dropped rather than sent as garbage.
func (e Event) payload() (map[string]any, bool) {
	switch e.Kind {
	case EventReady:
		ready := e.Ready
		if ready == nil {
			ready = &Ready{}
		}
		models := make([]map[string]any, 0, len(ready.Models))
		for _, model := range ready.Models {
			models = append(models, map[string]any{
				"id":            model.ID,
				"contextLength": model.ContextLength,
			})
		}
		thinking := ready.Thinking
		if thinking == nil {
			thinking = []string{}
		}
		theme := ready.Theme
		if theme == "" {
			theme = "dark"
		}
		return map[string]any{
			"kind":       string(EventReady),
			"model":      ready.Model,
			"level":      ready.Level,
			"contextMax": ready.ContextMax,
			"models":     models,
			"thinking":   thinking,
			"cwd":        ready.Cwd,
			"provider":   ready.Provider,
			"protocol":   ready.Protocol,
			"theme":      theme,
		}, true

	case EventAgent:
		if e.Agent == nil {
			return nil, false
		}
		event, ok := e.Agent.payload()
		if !ok {
			return nil, false
		}
		return map[string]any{"kind": string(EventAgent), "event": event}, true

	case EventTurnDone:
		done := e.TurnDone
		if done == nil {
			done = &TurnDone{}
		}
		reason := done.Reason
		if reason == "" {
			reason = "complete"
		}
		payload := map[string]any{
			"kind":             string(EventTurnDone),
			"reason":           reason,
			"requests":         done.Requests,
			"priced":           done.Cost != nil,
			"promptTokens":     done.PromptTokens,
			"completionTokens": done.CompletionTokens,
			"totalTokens":      done.TotalTokens,
			"cachedTokens":     done.CachedTokens,
		}
		if done.Cost != nil {
			payload["cost"] = *done.Cost
		} else {
			payload["cost"] = nil
		}
		return payload, true

	case EventError:
		message := e.Message
		if message == "" {
			message = "unknown error"
		}
		return map[string]any{"kind": string(EventError), "message": message}, true

	case EventSession:
		return map[string]any{"kind": string(EventSession), "path": e.Path}, true

	case EventSessionCleared:
		return map[string]any{"kind": string(EventSessionCleared)}, true

	case EventLoaded:
		entries := make([]map[string]any, 0, len(e.Entries))
		for _, entry := range e.Entries {
			entries = append(entries, entry.payload())
		}
		return map[string]any{"kind": string(EventLoaded), "entries": entries}, true

	case EventSessions:
		rows := make([]map[string]any, 0, len(e.Sessions))
		for _, row := range e.Sessions {
			rows = append(rows, map[string]any{
				"path":    row.Path,
				"title":   row.Title,
				"updated": row.Updated,
				"cwd":     row.Cwd,
			})
		}
		return map[string]any{"kind": string(EventSessions), "sessions": rows}, true
	}
	return nil, false
}

func (a AgentEvent) payload() (map[string]any, bool) {
	switch a.Type {
	case AgentTurnStart:
		return map[string]any{"type": a.Type, "index": a.Index}, true
	case AgentTurnEnd:
		return map[string]any{"type": a.Type, "index": a.Index}, true
	case AgentText:
		return map[string]any{"type": a.Type, "text": a.Text}, true
	case AgentThinking:
		return map[string]any{"type": a.Type, "text": a.Text}, true
	case AgentToolRequested:
		return map[string]any{
			"type":      a.Type,
			"id":        a.ID,
			"name":      a.Name,
			"arguments": a.Arguments,
		}, true
	case AgentToolFinished:
		out := map[string]any{
			"type":    a.Type,
			"id":      a.ID,
			"name":    a.Name,
			"output":  a.Output,
			"isError": a.IsError,
		}
		if a.DurationMS > 0 {
			out["durationMs"] = a.DurationMS
		}
		return out, true
	case AgentFinished:
		reason := a.Reason
		if reason == "" {
			reason = "ready"
		}
		return map[string]any{"type": a.Type, "reason": reason}, true
	case AgentNotice:
		return map[string]any{"type": a.Type, "text": a.Text}, true
	case AgentAborted:
		return map[string]any{"type": a.Type}, true
	case AgentAttempt:
		attempt := a.Attempt
		if attempt == "" {
			attempt = "failed"
		}
		return map[string]any{"type": a.Type, "attempt": attempt, "text": a.Text}, true
	}
	return nil, false
}

func (e LoadedEntry) payload() map[string]any {
	switch e.Kind {
	case "notice":
		level := e.Level
		if level != "warning" && level != "error" {
			level = "info"
		}
		out := map[string]any{"kind": "notice", "level": level, "text": e.Text}
		if e.Attempt != "" {
			out["attempt"] = e.Attempt
		}
		return out
	case "tool":
		state := e.State
		if state != "running" && state != "error" {
			state = "ok"
		}
		out := map[string]any{
			"kind":      "tool",
			"callId":    e.CallID,
			"name":      e.Name,
			"arguments": e.Arguments,
			"output":    e.Output,
			"state":     state,
		}
		if e.DurationMS != nil {
			out["durationMs"] = *e.DurationMS
		} else {
			out["durationMs"] = nil
		}
		if e.Attempt != "" {
			out["attempt"] = e.Attempt
		}
		return out
	case "user", "assistant", "thinking":
		out := map[string]any{"kind": e.Kind, "text": e.Text}
		if e.Attempt != "" {
			out["attempt"] = e.Attempt
		}
		return out
	}
	return map[string]any{"kind": e.Kind, "text": e.Text}
}
