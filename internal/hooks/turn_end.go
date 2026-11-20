package hooks

import (
	"context"
	"errors"
)

// ClassifyTurnEndEvent maps the outcome of one agent turn onto the lifecycle
// event that should close it. It mirrors server.ClassifyTurnEnd's three-way
// split, but returns the hook event name so callers can emit the result
// directly:
//
//	event := hooks.Event(hooks.ClassifyTurnEndEvent(canceled, err))
//
// A cancellation — an explicit `canceled` signal or a (possibly wrapped or
// joined) context.Canceled error — is an interruption. Any other error is a
// failure, and a nil error with no cancellation is a clean completion.
//
// Deadlines are deliberately NOT cancellations (matching the server-side
// classifier): a context.DeadlineExceeded, wrapped or not, is a failure. An
// error that merely contains the words "context canceled" without being the
// sentinel is also a failure, so a genuine failure is never misreported as a
// user interruption.
func ClassifyTurnEndEvent(canceled bool, err error) string {
	if canceled || errors.Is(err, context.Canceled) {
		return string(EventInterrupt)
	}
	if err != nil {
		return string(EventStopFailure)
	}
	return string(EventTurnEnd)
}
