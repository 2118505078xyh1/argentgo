// Package automation schedules agent prompts: a cron- or interval-based task
// list, a durable JSON store, and a daemon that fires due tasks through an
// injected executor.
//
// A task is a small JSON document:
//
//	{"id":"nightly","name":"nightly review",
//	 "schedule":{"kind":"cron","expr":"0 3 * * *"},
//	 "prompt":"review yesterday's commits","cwd":"/repo",
//	 "enabled":true,"missed":"run_once","timeout":"30m"}
//
// Everything here is sequential dependency-free Go: the daemon never spawns
// the agent itself, it calls an Executor, so the CLI (or a test) wires the real
// agent loop in afterwards.
package automation

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

// Missed policies decide what happens when the daemon wakes up after a
// scheduled slot has already passed.
const (
	// MissedRunOnce runs the task once immediately, then resumes the schedule.
	MissedRunOnce = "run_once"
	// MissedSkip drops the missed slot and waits for the next one.
	MissedSkip = "skip"
)

// Duration is a JSON-friendly time.Duration. It marshals as a duration string
// ("30m") and accepts either a duration string or a number of seconds when
// decoding, so hand-written config files stay readable.
type Duration time.Duration

// Std returns the value as a time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// String renders the duration the way time.Duration does.
func (d Duration) String() string { return time.Duration(d).String() }

// Or returns the duration, or def when it is not positive.
func (d Duration) Or(def time.Duration) time.Duration {
	if v := time.Duration(d); v > 0 {
		return v
	}
	return def
}

// MarshalJSON writes the duration as a string.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// UnmarshalJSON accepts "45s", 90 (seconds), "" and null.
func (d *Duration) UnmarshalJSON(data []byte) error {
	text := strings.TrimSpace(string(data))
	if text == "null" {
		*d = 0
		return nil
	}
	if strings.HasPrefix(text, `"`) {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		if strings.TrimSpace(s) == "" {
			*d = 0
			return nil
		}
		v, err := time.ParseDuration(strings.TrimSpace(s))
		if err != nil {
			return fmt.Errorf("automation: duration %q: %w", s, err)
		}
		*d = Duration(v)
		return nil
	}
	var seconds float64
	if err := json.Unmarshal(data, &seconds); err != nil {
		return fmt.Errorf("automation: duration must be a string like \"30m\" or a number of seconds: %w", err)
	}
	*d = Duration(time.Duration(seconds * float64(time.Second)))
	return nil
}

// Schedule says when a task fires.
type Schedule struct {
	// Kind is "cron" or "interval". An empty kind is inferred from Expr.
	Kind string `json:"kind"`
	// Expr is a five-field cron expression or a Go duration ("30m", "1d").
	Expr string `json:"expr"`
}

// Normalised schedule kinds.
const (
	KindCron     = "cron"
	KindInterval = "interval"
)

// kind resolves an empty or misspelled kind from the expression shape.
func (s Schedule) kind() string {
	switch strings.ToLower(strings.TrimSpace(s.Kind)) {
	case KindCron:
		return KindCron
	case KindInterval, "every":
		return KindInterval
	case "":
		if len(strings.Fields(strings.TrimSpace(s.Expr))) == 5 {
			return KindCron
		}
		return KindInterval
	}
	return strings.ToLower(strings.TrimSpace(s.Kind))
}

// Validate reports whether the schedule can be parsed.
func (s Schedule) Validate() error {
	switch s.kind() {
	case KindCron:
		_, err := ParseCron(s.Expr)
		return err
	case KindInterval:
		_, err := ParseInterval(s.Expr)
		return err
	}
	return fmt.Errorf("automation: unknown schedule kind %q (use cron or interval)", s.Kind)
}

// Next returns the first firing time strictly after t. A zero time means the
// expression never fires (for example "0 0 31 2 *") or cannot be parsed.
func (s Schedule) Next(t time.Time) time.Time {
	if t.IsZero() {
		return time.Time{}
	}
	switch s.kind() {
	case KindCron:
		c, err := ParseCron(s.Expr)
		if err != nil {
			return time.Time{}
		}
		return c.Next(t)
	case KindInterval:
		// "1d" means "same time tomorrow", so advance the calendar instead
		// of adding a fixed 24h, which drifts across DST transitions.
		if days, ok := parseDays(s.Expr); ok {
			return addCalendarDays(t, days)
		}
		d, err := ParseInterval(s.Expr)
		if err != nil {
			return time.Time{}
		}
		return t.Add(d)
	}
	return time.Time{}
}

// String renders the schedule for logs and for read-only task lists. The
// canonical "kind expr" is always kept as a prefix; a valid cron expression
// additionally gets a human-readable summary in parentheses so a CLI/TUI list
// can show what the schedule actually means without a separate lookup.
func (s Schedule) String() string {
	kind := s.kind()
	if kind == "" {
		kind = s.Kind
	}
	base := kind + " " + strings.TrimSpace(s.Expr)
	if kind == KindCron {
		if d, err := DescribeCron(s.Expr, time.Time{}, 0); err == nil && d.Summary != "" {
			return base + " (" + d.Summary + ")"
		}
	}
	return base
}

// Spec is one scheduled task plus its persisted run state.
type Spec struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Schedule Schedule `json:"schedule"`
	Prompt   string   `json:"prompt"`
	Cwd      string   `json:"cwd,omitempty"`
	Enabled  bool     `json:"enabled"`
	// Missed is "run_once" (default) or "skip".
	Missed  string   `json:"missed,omitempty"`
	Timeout Duration `json:"timeout,omitzero"`
	// NextDue is the slot the daemon is waiting for. It is seeded by the store
	// and advanced after every run, so a task never fires twice for one slot.
	NextDue    time.Time  `json:"nextDue,omitzero"`
	LastRun    *time.Time `json:"lastRun,omitempty"`
	LastStatus string     `json:"lastStatus,omitempty"`
	LastLog    string     `json:"lastLog,omitempty"`
	// Warnings 报告在声明性内容中发现的不可见字符。
	// It is metadata only: it is never serialised and never blocks a task.
	Warnings []string `json:"-"`
}

// UnmarshalJSON applies the defaults a hand-written task file may omit:
// enabled=true, missed="run_once", and an id derived from the name.
func (s *Spec) UnmarshalJSON(data []byte) error {
	type specJSON struct {
		ID         string     `json:"id"`
		Name       string     `json:"name"`
		Schedule   Schedule   `json:"schedule"`
		Prompt     string     `json:"prompt"`
		Cwd        string     `json:"cwd"`
		Enabled    *bool      `json:"enabled"`
		Missed     string     `json:"missed"`
		Timeout    Duration   `json:"timeout"`
		NextDue    time.Time  `json:"nextDue"`
		LastRun    *time.Time `json:"lastRun"`
		LastStatus string     `json:"lastStatus"`
		LastLog    string     `json:"lastLog"`
	}
	var raw specJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*s = Spec{
		ID:         raw.ID,
		Name:       raw.Name,
		Schedule:   raw.Schedule,
		Prompt:     raw.Prompt,
		Cwd:        raw.Cwd,
		Enabled:    raw.Enabled == nil || *raw.Enabled,
		Missed:     raw.Missed,
		Timeout:    raw.Timeout,
		NextDue:    raw.NextDue,
		LastRun:    raw.LastRun,
		LastStatus: raw.LastStatus,
		LastLog:    raw.LastLog,
	}
	if s.Missed == "" {
		s.Missed = MissedRunOnce
	}
	if s.ID == "" {
		s.ID = slug(s.Name)
	}
	s.Warnings = security.ScanArtifact(string(data)).Warnings()
	return nil
}

// MissedPolicy normalises the missed field; unknown values behave like
// run_once so a typo never silently drops work.
func (s Spec) MissedPolicy() string {
	if strings.EqualFold(strings.TrimSpace(s.Missed), MissedSkip) {
		return MissedSkip
	}
	return MissedRunOnce
}

// Validate checks the fields the daemon relies on.
func (s Spec) Validate() error {
	if strings.TrimSpace(s.ID) == "" {
		return fmt.Errorf("automation: a task needs an id")
	}
	if strings.TrimSpace(s.Name) == "" {
		return fmt.Errorf("automation: task %q needs a name", s.ID)
	}
	if strings.TrimSpace(s.Prompt) == "" {
		return fmt.Errorf("automation: task %q needs a prompt", s.ID)
	}
	if err := s.Schedule.Validate(); err != nil {
		return fmt.Errorf("automation: task %q: %w", s.ID, err)
	}
	switch strings.ToLower(strings.TrimSpace(s.Missed)) {
	case "", MissedRunOnce, MissedSkip:
	default:
		return fmt.Errorf("automation: task %q has an unknown missed policy %q", s.ID, s.Missed)
	}
	if s.Timeout.Std() < 0 {
		return fmt.Errorf("automation: task %q has a negative timeout", s.ID)
	}
	return nil
}

// IsDue reports whether the task should run at now.
func (s Spec) IsDue(now time.Time) bool {
	if !s.Enabled || s.NextDue.IsZero() {
		return false
	}
	return !s.NextDue.After(now)
}

// TimeoutOr returns the run timeout, falling back to def.
func (s Spec) TimeoutOr(def time.Duration) time.Duration { return s.Timeout.Or(def) }

// slug turns a task name into a filesystem-safe id.
func slug(text string) string {
	var out strings.Builder
	for _, ch := range strings.ToLower(text) {
		switch {
		case ch >= 'a' && ch <= 'z', ch >= '0' && ch <= '9':
			out.WriteRune(ch)
		case ch == ' ' || ch == '-' || ch == '_' || ch == '\t' || ch == '\n':
			out.WriteByte('-')
		}
	}
	id := strings.Trim(out.String(), "-")
	if id == "" {
		return "task"
	}
	return id
}
