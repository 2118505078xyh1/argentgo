package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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

// RetryAttempt describes one retry decision inside RetryPolicy.Do. Reason
// explains why the attempt is retried; it never carries a response body or a
// credential.
type RetryAttempt struct {
	Attempt    int
	StatusCode int
	Cause      string
	Reason     Reason
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

// clipErrorClass bounds an error class by runes. Byte slicing could cut a
// multibyte message (e.g. Chinese) in the middle of a UTF-8 sequence.
func clipErrorClass(text string) string {
	return clipRedacted(text, 256)
}

// streamIntegrity tracks whether a reply reached its protocol terminator and
// whether any tool call had to be completed with an empty argument object
// because the streamed arguments were empty or malformed. Reaching EOF in
// that state means the provider cut the reply off mid-tool-call: dispatching
// it would run a real tool with invented "{}" arguments. Text-only truncation
// stays tolerated so gateways that omit the terminator keep working.
type streamIntegrity struct {
	terminated     bool
	incompleteTool bool
}

// toolArguments completes streamed arguments, flagging a fallback to "{}" so
// an unterminated stream can reject the reply rather than dispatch a tool with
// arguments the provider never finished sending.
func (s *streamIntegrity) toolArguments(arguments string) string {
	if strings.TrimSpace(arguments) == "" || !json.Valid([]byte(arguments)) {
		s.incompleteTool = true
		return "{}"
	}
	return arguments
}

// eofError reports a truncation that must fail the attempt. marker is the
// protocol's own terminator, included in the error for diagnostics.
func (s *streamIntegrity) eofError(marker string) error {
	if s.terminated || !s.incompleteTool {
		return nil
	}
	return streamError("stream ended without %s while a tool call was incomplete", marker)
}

// streamError wraps a mid-stream failure with the sentinel the session layer
// classifies as a stream-error attempt.
func streamError(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrStreamError}, args...)...)
}
