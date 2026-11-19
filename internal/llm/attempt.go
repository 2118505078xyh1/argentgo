package llm

import (
	"context"
	"errors"
	"fmt"
)

// ErrStreamError marks a failure that happened after the provider accepted the
// request, while the answer was still streaming. The session layer uses it to
// record a stream-error attempt rather than a request failure.
var ErrStreamError = errors.New("provider stream error")

// AttemptKind is the stable reason attached to a provider attempt record.
type AttemptKind string

const (
	AttemptSuccess     AttemptKind = "success"
	AttemptFailed      AttemptKind = "failed"
	AttemptRetry       AttemptKind = "retry"
	AttemptCancelled   AttemptKind = "cancelled"
	AttemptStreamError AttemptKind = "stream_error"
)

// AttemptRecord is a redacted attempt notification for the session layer. It
// carries no prompt text, response body or credential material.
type AttemptRecord struct {
	Kind       AttemptKind
	Protocol   string
	Model      string
	Attempt    int
	StatusCode int
	ErrorClass string
}

// RetryAttempt describes one retry decision inside RetryPolicy.Do.
type RetryAttempt struct {
	Attempt    int
	StatusCode int
	Cause      string
}

// ClassifyError maps a provider error onto an attempt kind. Cancellation wins
// over stream errors so an aborted mid-stream read is not counted as a failed
// model answer.
func ClassifyError(err error) AttemptKind {
	switch {
	case err == nil:
		return AttemptSuccess
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return AttemptCancelled
	case errors.Is(err, ErrStreamError):
		return AttemptStreamError
	default:
		return AttemptFailed
	}
}

// NewAttemptRecord builds a record whose ErrorClass has already been redacted
// and shortened, so hooks can log it directly.
func NewAttemptRecord(kind AttemptKind, protocol, model string, err error) AttemptRecord {
	record := AttemptRecord{Kind: kind, Protocol: protocol, Model: model}
	// Cancellation is a stop reason, not a provider error class.
	if err != nil && kind != AttemptCancelled {
		record.ErrorClass = clipErrorClass(RedactError(err))
	}
	return record
}

func emitAttempt(hook func(AttemptRecord), record AttemptRecord) {
	if hook != nil {
		hook(record)
	}
}

func clipErrorClass(text string) string {
	const limit = 256
	if len(text) <= limit {
		return text
	}
	return text[:limit-1] + "…"
}

// streamError wraps a mid-stream failure with the sentinel the session layer
// classifies as a stream-error attempt.
func streamError(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrStreamError}, args...)...)
}
