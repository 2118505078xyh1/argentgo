package tools

import "context"

// BackgroundReport describes one request to promote a shell command to a
// background job. It lets a frontend or app explain why a command was killed on
// timeout instead of being promoted, without importing the jobs registry.
type BackgroundReport struct {
	Command       string `json:"command"`
	RequestedMS   int    `json:"requested_ms"`
	JobsAvailable bool   `json:"jobs_available"`
	Status        string `json:"status"`
}

// BackgroundReporter receives one report per bash call that sets
// background_after_ms. It must be cheap and must not run the command.
type BackgroundReporter func(BackgroundReport)

type backgroundReporterKey struct{}

// SetBackgroundReporter installs the optional report hook. Passing nil removes
// it. The default is no reporting, which keeps existing runs unchanged.
func (r *Registry) SetBackgroundReporter(fn BackgroundReporter) {
	r.backgroundReporter = fn
}

func reportBackground(ctx context.Context, command string, requestedMS int) {
	reporter, _ := ctx.Value(backgroundReporterKey{}).(BackgroundReporter)
	if reporter == nil || requestedMS <= 0 {
		return
	}
	available := jobRegistry(ctx) != nil
	status := "unavailable"
	if available {
		status = "enabled"
	}
	reporter(BackgroundReport{
		Command:       command,
		RequestedMS:   requestedMS,
		JobsAvailable: available,
		Status:        status,
	})
}

func withBackgroundReporter(ctx context.Context, reporter BackgroundReporter) context.Context {
	if reporter == nil {
		return ctx
	}
	return context.WithValue(ctx, backgroundReporterKey{}, reporter)
}
