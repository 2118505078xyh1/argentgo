// Package hooks parses Claude-Code-compatible hook documents, checks their
// SHA-256 trust, and dispatches lifecycle events to shell commands.
package hooks

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// Event is one lifecycle event a hook can subscribe to.
type Event string

// Event constants are the lifecycle points a hook can subscribe to. The names
// match Claude Code, so an existing hook document keeps firing. The comment on
// each constant records its intended trigger point; the full set is available
// as a report from ReportTriggers.
const (
	// SessionStart fires once when a session begins, before the first prompt.
	SessionStart Event = "SessionStart"
	// UserPromptSubmit fires when the user sends a prompt.
	UserPromptSubmit Event = "UserPromptSubmit"
	// PreToolUse fires before a tool call; exit status 2 may block it.
	PreToolUse Event = "PreToolUse"
	// PermissionRequest fires when a tool call needs an approval decision.
	PermissionRequest Event = "PermissionRequest"
	// PostToolUse fires after a tool call succeeds.
	PostToolUse Event = "PostToolUse"
	// PostToolUseFailure fires after a tool call fails.
	PostToolUseFailure Event = "PostToolUseFailure"
	// Notification fires for informational messages shown to the user.
	Notification Event = "Notification"
	// PreCompact fires just before the conversation is compacted.
	PreCompact Event = "PreCompact"
	// SessionEnd fires when a session ends, including on cancellation.
	SessionEnd Event = "SessionEnd"
	// SubagentStop fires when a subagent finishes its run.
	SubagentStop Event = "SubagentStop"
	// Stop fires when the assistant finishes a turn.
	Stop Event = "Stop"
	// FileChanged fires when a tool creates, modifies or deletes a file. The
	// affected path is carried in Payload.Path.
	FileChanged Event = "FileChanged"
)

// AllEvents lists every supported event.
var AllEvents = []Event{
	SessionStart,
	UserPromptSubmit,
	PreToolUse,
	PermissionRequest,
	PostToolUse,
	PostToolUseFailure,
	Notification,
	PreCompact,
	SessionEnd,
	SubagentStop,
	Stop,
	FileChanged,
}

const (
	// defaultTimeout bounds one hook command.
	defaultTimeout = 30 * time.Second
	// minTimeout is the floor a document's timeoutMs is clamped to.
	minTimeout = 100 * time.Millisecond
)

// ParseEvent maps a document event name to an Event.
func ParseEvent(name string) (Event, bool) {
	for _, event := range AllEvents {
		if string(event) == name {
			return event, true
		}
	}
	return "", false
}

func (e Event) String() string { return string(e) }

// TriggerReport pairs a lifecycle event with the point at which callers should
// fire it. It is the machine-readable form of the trigger-timing documentation
// on the event constants, useful for diagnostics and for auditing which events
// a hook document can subscribe to.
type TriggerReport struct {
	Event   Event
	Trigger string // when the event should be dispatched
	Payload string // payload fields that carry event-specific detail
}

// ReportTriggers returns one row per supported event in AllEvents order. The
// report is data only: dispatching an event is the caller's responsibility. The
// app layer fires the session/prompt/tool events around one Execute; a caller
// that mutates files should fire FileChanged with the affected Payload.Path.
func ReportTriggers() []TriggerReport {
	rows := make([]TriggerReport, 0, len(AllEvents))
	for _, event := range AllEvents {
		row := TriggerReport{Event: event}
		switch event {
		case SessionStart:
			row.Trigger = "once when a session begins, before the first prompt"
		case UserPromptSubmit:
			row.Trigger = "when the user submits a prompt"
			row.Payload = "Prompt"
		case PreToolUse:
			row.Trigger = "before each tool call; exit 2 blocks the call"
			row.Payload = "Tool, Args, CallID"
		case PermissionRequest:
			row.Trigger = "when a tool call needs an approval decision"
			row.Payload = "Tool, Args, CallID"
		case PostToolUse:
			row.Trigger = "after a tool call succeeds"
			row.Payload = "Tool, Args, Output, CallID"
		case PostToolUseFailure:
			row.Trigger = "after a tool call fails"
			row.Payload = "Tool, Args, Error, CallID"
		case Notification:
			row.Trigger = "when an informational notice is shown to the user"
			row.Payload = "Prompt"
		case PreCompact:
			row.Trigger = "just before the conversation is compacted"
		case SessionEnd:
			row.Trigger = "when a session ends, including on cancellation"
		case SubagentStop:
			row.Trigger = "when a subagent finishes its run"
			row.Payload = "Output"
		case Stop:
			row.Trigger = "when the assistant finishes a turn"
			row.Payload = "Prompt, Output"
		case FileChanged:
			row.Trigger = "when a tool creates, modifies or deletes a file"
			row.Payload = "Path (the affected file)"
		}
		rows = append(rows, row)
	}
	return rows
}

// Command is one parsed hook command.
type Command struct {
	Event   Event
	Matcher string // empty or "*" matches everything
	Command string
	Timeout time.Duration
	OnError string // "block" (default), "allow" or "ignore"
	Async   bool

	// Priority orders hooks at dispatch time: higher values run first. It is
	// internal-only (json:"-") so the on-disk hook document format is
	// unchanged; document-parsed hooks all start at 0 and keep their
	// declaration order. Programmatic callers (e.g. built-in security hooks)
	// set a positive Priority to run ahead of user hooks.
	Priority int `json:"-"`

	// StopPropagation short-circuits dispatch: once a hook carrying this flag
	// has run, no lower-priority hook is executed. Like Priority it is
	// internal-only; a document hook never sets it, so existing documents
	// still run every matching hook.
	StopPropagation bool `json:"-"`
}

// BlocksOnFailure reports whether a failing command may block the action.
func (c Command) BlocksOnFailure() bool {
	return c.OnError != "allow" && c.OnError != "ignore"
}

// Config is the hooks parsed from one or more documents.
type Config struct {
	Commands []Command
}

// rawDocument mirrors a hook document. Unknown events are tolerated and
// skipped, matching Claude Code's forgiving loader.
type rawDocument struct {
	Hooks map[string][]json.RawMessage `json:"hooks"`
}

// rawCommand is one group or one command; the two shapes share fields.
type rawCommand struct {
	Type      string       `json:"type"`
	Command   string       `json:"command"`
	Matcher   string       `json:"matcher"`
	TimeoutMS *int64       `json:"timeoutMs"`
	OnError   string       `json:"onError"`
	Async     bool         `json:"async"`
	Hooks     []rawCommand `json:"hooks"`
}

// ParseConfig parses a hook document.
func ParseConfig(data []byte) (*Config, error) {
	var document rawDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("hooks: invalid document: %w", err)
	}
	if document.Hooks == nil {
		return nil, errors.New(`hooks: document has no "hooks" object`)
	}
	config := &Config{}
	for _, name := range sortedEventNames(document.Hooks) {
		event, ok := ParseEvent(name)
		if !ok {
			continue
		}
		for _, raw := range document.Hooks[name] {
			commands, err := parseGroup(event, raw)
			if err != nil {
				return nil, err
			}
			config.Commands = append(config.Commands, commands...)
		}
	}
	return config, nil
}

// LoadConfig reads and parses a hook document.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("hooks: read %s: %w", path, err)
	}
	config, err := ParseConfig(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return config, nil
}

func parseGroup(event Event, raw json.RawMessage) ([]Command, error) {
	var group rawCommand
	if err := json.Unmarshal(raw, &group); err != nil {
		return nil, fmt.Errorf("hooks: %s: %w", event, err)
	}
	matcher := strings.TrimSpace(group.Matcher)
	candidates := group.Hooks
	if len(candidates) == 0 {
		candidates = []rawCommand{group}
	}

	commands := make([]Command, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Type != "" && candidate.Type != "command" {
			continue
		}
		text := strings.TrimSpace(candidate.Command)
		if text == "" {
			continue
		}
		commands = append(commands, Command{
			Event:   event,
			Matcher: matcher,
			Command: text,
			Timeout: timeoutOf(candidate.TimeoutMS),
			OnError: onErrorOf(candidate.OnError),
			Async:   candidate.Async,
		})
	}
	return commands, nil
}

func timeoutOf(ms *int64) time.Duration {
	if ms == nil {
		return defaultTimeout
	}
	timeout := time.Duration(*ms) * time.Millisecond
	if timeout < minTimeout {
		return minTimeout
	}
	return timeout
}

func onErrorOf(value string) string {
	switch value {
	case "allow", "ignore", "block":
		return value
	default:
		return "block"
	}
}

// Merge appends another document's commands.
func (c *Config) Merge(other *Config) {
	if c == nil || other == nil {
		return
	}
	c.Commands = append(c.Commands, other.Commands...)
}

// ForEvent returns the commands for an event, filtered by the tool matcher.
func (c *Config) ForEvent(event Event, tool string) []Command {
	if c == nil {
		return nil
	}
	commands := make([]Command, 0, len(c.Commands))
	for _, command := range c.Commands {
		if command.Event != event {
			continue
		}
		if command.Matcher == "" || command.Matcher == "*" {
			commands = append(commands, command)
			continue
		}
		if tool == "" {
			continue
		}
		if globMatch(command.Matcher, tool) {
			commands = append(commands, command)
		}
	}
	return commands
}

// Validate reports the silent typos a forgiving parse would swallow.
func Validate(data []byte) []string {
	var document rawDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return []string{"invalid JSON: " + err.Error()}
	}
	if document.Hooks == nil {
		return []string{`hook document has no "hooks" object`}
	}

	var warnings []string
	for _, name := range sortedEventNames(document.Hooks) {
		if _, ok := ParseEvent(name); !ok {
			warnings = append(warnings, fmt.Sprintf("unknown event %q (ignored)", name))
			continue
		}
		for index, raw := range document.Hooks[name] {
			var group rawCommand
			if err := json.Unmarshal(raw, &group); err != nil {
				warnings = append(warnings, fmt.Sprintf("%s.%d: %v", name, index+1, err))
				continue
			}
			if group.Matcher != "" && strings.Contains(group.Matcher, " ") {
				warnings = append(warnings, fmt.Sprintf("%s.%d: matcher %q contains a space", name, index+1, group.Matcher))
			}
			candidates := group.Hooks
			if len(candidates) == 0 {
				candidates = []rawCommand{group}
			}
			for _, candidate := range candidates {
				if candidate.Type != "" && candidate.Type != "command" {
					warnings = append(warnings, fmt.Sprintf("%s.%d: type %q is not supported (ignored)", name, index+1, candidate.Type))
					continue
				}
				if strings.TrimSpace(candidate.Command) == "" {
					warnings = append(warnings, fmt.Sprintf("%s.%d: empty command (ignored)", name, index+1))
				}
				if candidate.OnError != "" && candidate.OnError != "block" &&
					candidate.OnError != "allow" && candidate.OnError != "ignore" {
					warnings = append(warnings, fmt.Sprintf("%s.%d: onError %q is not block|allow|ignore", name, index+1, candidate.OnError))
				}
				if candidate.TimeoutMS != nil && *candidate.TimeoutMS < 100 {
					warnings = append(warnings, fmt.Sprintf("%s.%d: timeoutMs %d is below the 100ms floor", name, index+1, *candidate.TimeoutMS))
				}
			}
		}
	}
	return warnings
}

func sortedEventNames(hooks map[string][]json.RawMessage) []string {
	names := make([]string, 0, len(hooks))
	for name := range hooks {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// globMatch matches "*" against any run of characters; everything else is
// literal. This is the matcher syntax hook documents use for tool names.
func globMatch(pattern, value string) bool {
	if pattern == "*" {
		return true
	}
	patternRunes := []rune(pattern)
	valueRunes := []rune(value)
	p, v := 0, 0
	star, starV := -1, 0
	for v < len(valueRunes) {
		switch {
		// The wildcard case must come first. If the literal-equality case ran
		// first, a '*' in the pattern would be consumed as a literal when the
		// value happened to contain '*' too (e.g. pattern "a*" against value
		// "a*b"), leaving no star to backtrack to and failing the match. Tool
		// names and file paths can carry a literal '*', so treat the pattern's
		// '*' as a wildcard unconditionally.
		case p < len(patternRunes) && patternRunes[p] == '*':
			star = p
			starV = v
			p++
		case p < len(patternRunes) && patternRunes[p] == valueRunes[v]:
			p++
			v++
		case star >= 0:
			p = star + 1
			starV++
			v = starV
		default:
			return false
		}
	}
	for p < len(patternRunes) && patternRunes[p] == '*' {
		p++
	}
	return p == len(patternRunes)
}
