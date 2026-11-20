package security

import (
	"context"
	"errors"
)

// ApprovalOutcome is the classified result of an Approver.Ask call.
//
// Approver.Ask returns a bare (bool, error). A plain bool collapses four very
// different situations into two values: a real "yes", a real "no", a broken or
// missing approval channel, and a cancelled/time-limited prompt. Headless runs
// and sub-agents are exactly where the human approval channel may be absent,
// so they must be able to tell "the operator approved" from "nobody answered".
// This four-valued vocabulary gives them that distinction without changing the
// Approver interface (which is used across the whole repository).
type ApprovalOutcome string

const (
	// ApprovalAllowed is a genuine affirmative answer: granted with no error.
	// It is the only outcome that permits a tool call.
	ApprovalAllowed ApprovalOutcome = "allowed"
	// ApprovalRejected is a definite negative answer with no error.
	ApprovalRejected ApprovalOutcome = "rejected"
	// ApprovalUnavailable means the approval channel could not produce an
	// answer (any non-context error). Fail-closed: never an allow.
	ApprovalUnavailable ApprovalOutcome = "unavailable"
	// ApprovalCancelled means the ask was cancelled or timed out. Fail-closed.
	ApprovalCancelled ApprovalOutcome = "cancelled"
)

// Allows reports whether the outcome is a genuine approval. It is the single
// fail-closed gate: only ApprovalAllowed returns true, so an unavailable or
// cancelled approval channel can never be normalized into consent.
func (o ApprovalOutcome) Allows() bool { return o == ApprovalAllowed }

// ClassifyApproval normalizes the raw (granted, err) pair returned by
// Approver.Ask into an ApprovalOutcome.
//
// The classification is deliberately fail-closed:
//   - a non-nil error is never an allow, even when granted is true;
//   - context.Canceled / context.DeadlineExceeded (including wrapped ones, via
//     errors.Is) become ApprovalCancelled;
//   - any other error becomes ApprovalUnavailable;
//   - only (true, nil) is ApprovalAllowed, and (false, nil) is ApprovalRejected.
//
// It is a pure function: no state, no side effects, safe for concurrent use.
func ClassifyApproval(granted bool, err error) ApprovalOutcome {
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return ApprovalCancelled
		}
		return ApprovalUnavailable
	}
	if granted {
		return ApprovalAllowed
	}
	return ApprovalRejected
}
