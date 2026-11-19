package session

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

// Attempt reasons recorded as append-only audit records. They never enter the
// model-visible message history.
const (
	AttemptFailed      = "failed"
	AttemptRetried     = "retried"
	AttemptCancelled   = "cancelled"
	AttemptStreamError = "stream_error"
)

// Attempt is the payload of a kind:"attempt" record. Error text is redacted
// before it is written; only the committed messages are reconstructed as model
// history.
type Attempt struct {
	Reason        string    `json:"reason"`
	Message       string    `json:"message,omitempty"`
	ErrorClass    string    `json:"errorClass,omitempty"`
	SourceSession string    `json:"sourceSession,omitempty"`
	SourceRecord  string    `json:"sourceRecord,omitempty"`
	Prompt        string    `json:"prompt,omitempty"`
	TS            time.Time `json:"-"`
}

// NewAttempt builds an attempt record from an error. A nil error records just
// the reason; a non-nil error contributes a redacted message and its Go type as
// a stable error class.
func NewAttempt(reason string, err error) Attempt {
	attempt := Attempt{Reason: strings.TrimSpace(reason)}
	if err == nil {
		return attempt
	}
	attempt.Message = err.Error()
	attempt.ErrorClass = fmt.Sprintf("%T", err)
	return attempt
}

// AppendAttempt records one attempt and returns the stored record.
func (s *Session) AppendAttempt(attempt Attempt) (Record, error) {
	attempt.Reason = strings.TrimSpace(attempt.Reason)
	if attempt.Reason == "" {
		return Record{}, errors.New("session: empty attempt reason")
	}
	attempt.Message = truncateRunes(security.Redact(attempt.Message), 240)
	attempt.ErrorClass = truncateRunes(security.Redact(attempt.ErrorClass), 80)
	attempt.Prompt = truncateRunes(security.Redact(attempt.Prompt), titleLimit)
	attempt.SourceSession = security.Redact(strings.TrimSpace(attempt.SourceSession))
	attempt.SourceRecord = security.Redact(strings.TrimSpace(attempt.SourceRecord))
	return s.AppendRecord(KindAttempt, attempt)
}

// RecordAttemptError is the error-shaped convenience wrapper around
// AppendAttempt, used by callers that only have an error to report.
func (s *Session) RecordAttemptError(reason string, err error) (Record, error) {
	return s.AppendAttempt(NewAttempt(reason, err))
}

// Attempts lists the attempt records on the active branch, oldest first.
func (s *Session) Attempts() ([]Attempt, error) {
	records, err := s.Records()
	if err != nil {
		return nil, err
	}
	out := make([]Attempt, 0)
	for _, record := range s.activePath(records) {
		if record.Kind != KindAttempt {
			continue
		}
		var attempt Attempt
		if jsonUnmarshal(record.Data, &attempt) != nil {
			continue
		}
		attempt.TS = record.TS
		out = append(out, attempt)
	}
	return out, nil
}

// Snippet is the short, redacted text used by tree and export views.
func (a Attempt) Snippet() string {
	if text := truncateRunes(a.Message, 60); text != "" {
		return text
	}
	if a.ErrorClass != "" {
		return a.ErrorClass
	}
	return a.Reason
}
