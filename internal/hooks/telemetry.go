package hooks

import "time"

// HookTelemetry is one completed hook observation. A Dispatcher.Telemetry
// callback receives one of these per hook executed by Dispatch/Run, from the
// hook's own goroutine (hooks run in parallel).
type HookTelemetry struct {
	Name     string        // hook identity: the command string
	Event    Event         // lifecycle event that triggered the hook
	Elapsed  time.Duration // wall-clock time spent running the hook
	Err      error         // error that prevented the hook from running; nil otherwise
	ExitCode int           // process exit status; -1 when it could not run or timed out
	TimedOut bool          // the hook exceeded its own timeout
	Blocked  bool          // the hook signalled a block (exit 2 on PreToolUse)
	Slow     bool          // Elapsed reached Dispatcher.SlowThreshold, when set
}

// Failed reports whether the hook did not complete successfully. It mirrors
// the conditions Summarize warns on: a launch error, a timeout, a block
// signal, or a non-zero exit status all count as failures.
func (r HookTelemetry) Failed() bool {
	return r.Err != nil || r.ExitCode != 0 || r.TimedOut || r.Blocked
}

// TelemetryFunc observes completed hooks. Implementations MUST be safe for
// concurrent use: each hook reports from its own goroutine while hooks run in
// parallel. A nil callback (the default) collects nothing and adds no work.
type TelemetryFunc func(HookTelemetry)

// WithTelemetry is a convenience constructor for callers that prefer a
// functional form over a struct literal. The zero-value Dispatcher stays
// valid; only the two telemetry fields are set here.
func WithTelemetry(callback TelemetryFunc, slowThreshold time.Duration) *Dispatcher {
	return &Dispatcher{Telemetry: callback, SlowThreshold: slowThreshold}
}

// reportTelemetry delivers one record. A panicking observer is contained just
// like a panicking hook callback so telemetry can never break dispatch.
func (d *Dispatcher) reportTelemetry(record HookTelemetry) {
	defer func() { _ = recover() }()
	d.Telemetry(record)
}
