package server

import (
	"context"
	"errors"
)

// TurnEndReason describes how an agent turn finished. The ACP session/prompt
// result historically exposed a single stopReason, which is enough to tell a
// "cancelled" reply from an "end_turn" reply but does not name the underlying
// cause. TurnEndReason is that three-way classification: a user/transport
// interruption, a genuine failure, or a clean completion.
//
// The value is carried on the terminal result as an additive, omitempty field,
// so a client that only understands stopReason keeps seeing the original wire
// shape and behaviour.
type TurnEndReason string

const (
	// TurnEndInterrupted means the user or the transport stopped the turn
	// (a (possibly wrapped) context.Canceled or an explicit cancellation).
	TurnEndInterrupted TurnEndReason = "interrupted"
	// TurnEndFailure means the turn ended in a genuine, non-cancellation error.
	TurnEndFailure TurnEndReason = "failure"
	// TurnEndComplete means the turn finished with no error.
	TurnEndComplete TurnEndReason = "complete"
)

// ClassifyTurnEnd maps the outcome of a Runner.Prompt call onto a
// TurnEndReason. A cancellation — either an explicit signal from the caller or
// a (possibly wrapped) context.Canceled error — is an interruption; any other
// error is a failure; a nil error with no cancellation is a clean completion.
//
// Deadlines are deliberately NOT cancellations (matching isCancelled): a
// context.DeadlineExceeded, wrapped or not, is a failure. An error that merely
// contains the words "context canceled" but is not the sentinel also stays a
// failure, so a failure is never misreported as a user interruption.
func ClassifyTurnEnd(err error, canceled bool) TurnEndReason {
	if canceled || errors.Is(err, context.Canceled) {
		return TurnEndInterrupted
	}
	if err != nil {
		return TurnEndFailure
	}
	return TurnEndComplete
}
