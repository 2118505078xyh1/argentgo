package hooks

import "sort"

// EventSummary is one row of a PendingEventSummary: the registration
// statistics for a single lifecycle event. It is pure data; building a summary
// never dispatches or mutates hooks.
type EventSummary struct {
	Event       Event
	Count       int // total hooks registered for the event
	WithMatcher int // hooks carrying a specific matcher (non-empty, not "*")
	Async       int // hooks marked for background execution
}

// PendingEventSummary reports, for every event with at least one registered
// hook, how many hooks it has, how many carry a specific matcher, and how many
// are async. This is a read-only diagnostic intended for CLI `doctor` output or
// debugging: it describes what is configured to fire, not what has fired.
//
// Rows follow AllEvents order; events outside AllEvents (set by programmatic
// callers, e.g. built-in hooks) come last in name order. An empty or nil config
// yields nil.
func (c *Config) PendingEventSummary() []EventSummary {
	if c == nil || len(c.Commands) == 0 {
		return nil
	}

	byEvent := make(map[Event]*EventSummary, len(c.Commands))
	for _, command := range c.Commands {
		row := byEvent[command.Event]
		if row == nil {
			row = &EventSummary{Event: command.Event}
			byEvent[command.Event] = row
		}
		row.Count++
		if command.Matcher != "" && command.Matcher != "*" {
			row.WithMatcher++
		}
		if command.Async {
			row.Async++
		}
	}

	rows := make([]EventSummary, 0, len(byEvent))
	for _, event := range AllEvents {
		if row := byEvent[event]; row != nil {
			rows = append(rows, *row)
			delete(byEvent, event)
		}
	}

	unknown := make([]Event, 0, len(byEvent))
	for event := range byEvent {
		unknown = append(unknown, event)
	}
	sort.Slice(unknown, func(i, j int) bool { return unknown[i] < unknown[j] })
	for _, event := range unknown {
		rows = append(rows, *byEvent[event])
	}
	return rows
}
