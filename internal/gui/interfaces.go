// Package gui's optional backends are described here as narrow interfaces.
//
// The bridge deliberately imports no other internal package: each capability
// that used to live behind a Tauri command is either implemented with the
// standard library under Options.StorageDir, or injected by internal/app when
// the GUI is wired up. Every field on gui.Options is optional. When an injected
// backend is nil, the corresponding command keeps answering the clear
// "not supported in the Go build" error instead of hanging or faking success.
//
// MEMORY and USAGE are the two exceptions: they have stdlib-backed defaults
// (NewFileMemory / NewFileUsage) which are used automatically when the field is
// nil, so those commands are real out of the box. An integrator that already
// owns a memory or usage system should inject it to override the default.
package gui

import "context"

// TurnHistory is the narrow checkpoint seam used by the undo / retry actions.
// internal/app (which owns the session store) injects it through Options. A nil
// value keeps both commands failing with an explicit "not wired" error rather
// than silently dropping the operator's action.
type TurnHistory interface {
	Undo(ctx context.Context, sid string, steps int) error
	Retry(ctx context.Context, sid string, steps int) error
}

// MemoryRow is one long-term memory entry shown in the Memory page.
// Scope is "user", "project" or "global"; Source is an optional human-readable
// location such as "memory/project__editor.md".
type MemoryRow struct {
	Scope  string  `json:"scope"`
	Key    string  `json:"key"`
	Value  string  `json:"value"`
	Source *string `json:"source,omitempty"`
}

// Memory lists, adds, forgets and searches long-term memories.
//
// Add appends (never overwrites an unrelated entry); Forget is idempotent.
// Search is a case-insensitive substring match over key and value and must
// return snippets with credential-shaped values redacted.
type Memory interface {
	List() ([]MemoryRow, error)
	Add(scope, key, value string) error
	Forget(scope, key string) error
	Search(query string) ([]MemoryRow, error)
}

// ModelUsage is one per-model roll-up in the Usage page.
type ModelUsage struct {
	Name         string  `json:"name"`
	Turns        int     `json:"turns"`
	PromptTokens int     `json:"promptTokens"`
	Cost         float64 `json:"cost"`
}

// DailyUsage is one per-day roll-up in the Usage page.
type DailyUsage struct {
	Day    string  `json:"day"`
	Turns  int     `json:"turns"`
	Tokens int     `json:"tokens"`
	Cost   float64 `json:"cost"`
}

// UsageSummary is the aggregate shown in the Usage page. The zero value is a
// valid empty report (non-nil slices are preferred so JSON renders []).
type UsageSummary struct {
	Turns            int          `json:"turns"`
	Requests         int          `json:"requests"`
	WarmRequests     int          `json:"warmRequests"`
	PromptTokens     int          `json:"promptTokens"`
	CompletionTokens int          `json:"completionTokens"`
	CachedTokens     int          `json:"cachedTokens"`
	MissedTokens     int          `json:"missedTokens"`
	CacheHitPercent  float64      `json:"cacheHitPercent"`
	Cost             float64      `json:"cost"`
	Models           []ModelUsage `json:"models"`
	Daily            []DailyUsage `json:"daily"`
}

// Usage produces the cumulative token/cost report.
type Usage interface {
	Summary() (UsageSummary, error)
}

// JobRow is one background job shown in the Jobs page. Status is one of
// "running", "succeeded", "failed" or "killed"; ExitCode is nil while running.
// Elapsed is seconds since the job started.
type JobRow struct {
	ID       string `json:"id"`
	Command  string `json:"command"`
	Status   string `json:"status"`
	ExitCode *int   `json:"exitCode"`
	Elapsed  int64  `json:"elapsed"`
}

// Jobs manages background processes. Start returns the new job id, Tail returns
// the last lines of a job log and Stop kills a job by id.
type Jobs interface {
	List() ([]JobRow, error)
	Tail(id string, lines int) (string, error)
	Stop(id string) error
	Start(command string) (string, error)
}

// AutomationRow is one scheduled task shown in the Automations page. Kind is
// "prompt" or "workflow"; NextRun, LastStatus and LastLog are display strings.
type AutomationRow struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Schedule   string  `json:"schedule"`
	Kind       string  `json:"kind"`
	Action     string  `json:"action"`
	Enabled    bool    `json:"enabled"`
	NextRun    string  `json:"nextRun"`
	LastStatus *string `json:"lastStatus"`
	LastLog    *string `json:"lastLog"`
}

// Automation manages scheduled tasks. Preview explains a schedule expression
// without creating anything; Log returns one task's run log.
type Automation interface {
	List() ([]AutomationRow, error)
	Create(name, schedule, prompt string) error
	Toggle(id string) error
	Delete(id string) error
	Run(id string) error
	Log(id string) (string, error)
	Preview(schedule string) (string, error)
}

// WorkflowValidation is the read-only validation badge of a discovered
// workflow. Steps is meaningful only when OK is true.
type WorkflowValidation struct {
	OK    bool   `json:"ok"`
	Steps int    `json:"steps,omitempty"`
	Error string `json:"error,omitempty"`
}

// WorkflowRow is one workflow definition found in the workspace.
type WorkflowRow struct {
	Path       string             `json:"path"`
	Name       string             `json:"name"`
	Validation WorkflowValidation `json:"validation"`
}

// Workflows discovers workflow definitions. List feeds the read-only page;
// Show returns the source of one definition and is reserved for a detail view.
type Workflows interface {
	List() ([]WorkflowRow, error)
	Show(path string) (string, error)
}

// DocumentRow is one office document found in the workspace.
type DocumentRow struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// Documents lists office documents and extracts their text.
type Documents interface {
	List() ([]DocumentRow, error)
	Read(path string) (string, error)
}

// PtyRow is one live pseudoterminal session shown in the Terminal page.
type PtyRow struct {
	ID      string `json:"id"`
	Closed  bool   `json:"closed"`
	Exited  bool   `json:"exited"`
	Elapsed int64  `json:"elapsed"`
}

// PTY manages persistent pseudoterminals. Spawn returns a session id; Write
// sends raw input; Read returns whatever the terminal produced; Kill ends it.
type PTY interface {
	Spawn(command string, cols, rows int) (string, error)
	Write(id, text string) error
	Read(id string) (string, error)
	Kill(id string) error
	List() ([]PtyRow, error)
}
