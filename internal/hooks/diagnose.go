package hooks

// Config completeness diagnostics.
//
// Validate (in hooks.go) inspects the raw JSON document and catches silent
// parse-time typos. This file complements it with a check that no document can
// trigger: Async and StopPropagation are internal-only (json:"-") flags set by
// programmatic callers (built-in hooks), so they are invisible to Validate.
// DiagnoseConfig is pure — it reads nothing, spawns nothing and never mutates
// the config; findings are returned, never logged or panicked.

import "fmt"

// IssueKind classifies one completeness problem found in a Config.
type IssueKind string

const (
	// IssueAsyncStopPropagation marks a command that is both Async and
	// StopPropagation: a background hook that still ends the claim walk.
	IssueAsyncStopPropagation IssueKind = "async-stop-propagation"
)

// ConfigIssue is one finding from DiagnoseConfig. Index is the 0-based position
// of the offending command in Config.Commands; Event and Command copy its event
// and text so callers can locate the hook without re-reading the config.
// Message is a stable, human-readable explanation suitable for doctor output or
// UI warnings.
type ConfigIssue struct {
	Kind    IssueKind
	Event   Event
	Index   int
	Command string
	Message string
}

// String renders an issue for direct display: "[kind] Event[index]: message".
func (i ConfigIssue) String() string {
	return fmt.Sprintf("[%s] %s[%d]: %s", i.Kind, i.Event, i.Index, i.Message)
}

// DiagnoseConfig reports completeness problems in an already-parsed Config.
// It is a read-only diagnostic: nothing is dispatched or mutated, and a nil or
// empty config yields an empty (non-nil) slice.
//
// It checks the one dimension Validate cannot reach — the combination of the
// internal-only Async and StopPropagation flags. A command carrying both ends
// claimedCommands' walk and shields every later hook, but Run never observes
// the outcome of an async hook (a deciding hook must answer synchronously).
// The effect is later hooks silently skipped on the strength of a fire-and-
// forget job, which is almost always unintended.
func DiagnoseConfig(c *Config) []ConfigIssue {
	issues := make([]ConfigIssue, 0)
	if c == nil {
		return issues
	}
	for index, command := range c.Commands {
		if !command.Async || !command.StopPropagation {
			continue
		}
		issues = append(issues, ConfigIssue{
			Kind:    IssueAsyncStopPropagation,
			Event:   command.Event,
			Index:   index,
			Command: command.Command,
			Message: fmt.Sprintf(
				"async command %q stops propagation: later %s hooks are skipped, but Run never observes an async decision; make the deciding hook synchronous or remove StopPropagation",
				command.Command, command.Event,
			),
		})
	}
	return issues
}
