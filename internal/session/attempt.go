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

// kindProgress is the record kind for durable assistant-progress checkpoints.
// The other record Kind constants live in session.go, which this change does
// not own, so the progress kind is deliberately kept local to this file.
const kindProgress = "progress"

// Bounds for one progress checkpoint. A checkpoint is written after every
// streamed chunk or tool batch, so records must stay small. Limits are in runes
// (not bytes) so a multi-byte UTF-8 rune is never cut in half.
const (
	maxProgressTextRunes = 8 << 10 // 8 KiB of assistant text per checkpoint
	maxProgressToolCalls = 64      // tool calls kept per checkpoint
	maxProgressToolRunes = 256     // characters kept per tool call name/argument
)

// Progress is the payload of a kind:"progress" record: committed progress
// WITHIN a turn, so a crash mid-run can resume from the last checkpoint rather
// than only from the last complete assistant message. Like Attempt, it is a
// durability/audit record and is never rebuilt into model-visible history.
//
// Turn is 1-based and should match the turn the assistant reply belongs to.
// TS is filled from the enclosing record on read; the stored value is just a
// convenience for callers that construct a Progress directly.
type Progress struct {
	Turn          int       `json:"turn"`
	AssistantText string    `json:"assistantText,omitempty"`
	ToolCalls     []string  `json:"toolCalls,omitempty"`
	TS            time.Time `json:"ts"`
}

// capProgressRunes clips text to limit runes without otherwise changing it.
func capProgressRunes(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

// sanitizeProgress bounds and redacts a progress payload before it reaches the
// transcript. Redaction runs before truncation so a credential that straddles
// the cap can never survive as a partially-cut secret. Tool calls are bounded
// in both count and length to keep a single record small.
func sanitizeProgress(p Progress) Progress {
	p.AssistantText = capProgressRunes(security.Redact(p.AssistantText), maxProgressTextRunes)
	if len(p.ToolCalls) > maxProgressToolCalls {
		p.ToolCalls = append([]string(nil), p.ToolCalls[:maxProgressToolCalls]...)
	} else if p.ToolCalls != nil {
		p.ToolCalls = append([]string(nil), p.ToolCalls...)
	}
	for i, tool := range p.ToolCalls {
		p.ToolCalls[i] = capProgressRunes(security.Redact(tool), maxProgressToolRunes)
	}
	return p
}

// AppendProgress appends one durable progress checkpoint via the normal append
// path and returns an error only when the record could not be written. The
// payload is redacted and bounded first, so callers may pass raw streamed text.
func (s *Session) AppendProgress(p Progress) error {
	_, err := s.AppendRecord(kindProgress, sanitizeProgress(p))
	return err
}

// progressScope returns the records a progress query should scan: the active
// branch when one is resolvable, otherwise every record in file order.
func progressScope(s *Session, records []Record) []Record {
	if path := s.activePath(records); len(path) > 0 {
		return path
	}
	return records
}

// latestProgressRecord returns the last progress record in a file-ordered
// slice together with its index, so callers can compare its position with a
// committed assistant message. The boolean reports whether one was found.
func latestProgressRecord(records []Record) (Progress, int, bool) {
	var found Progress
	idx := -1
	for i, record := range records {
		if record.Kind != kindProgress {
			continue
		}
		var p Progress
		if jsonUnmarshal(record.Data, &p) != nil {
			continue
		}
		p.TS = record.TS // the record timestamp is authoritative on read
		found, idx = p, i
	}
	return found, idx, idx >= 0
}

// LatestProgress returns the most recent progress checkpoint on the active
// branch (or the newest record when no active branch can be resolved). The
// boolean reports whether any checkpoint was found. It is the raw query; use
// RecoverProgress for staleness-checked crash recovery.
func (s *Session) LatestProgress() (Progress, bool) {
	records, err := s.Records()
	if err != nil {
		return Progress{}, false
	}
	progress, _, ok := latestProgressRecord(progressScope(s, records))
	return progress, ok
}

// ProgressCount reports how many progress checkpoints are stored, including
// ones on inactive branches. It is intended for tests and metrics.
func (s *Session) ProgressCount() int {
	records, err := s.Records()
	if err != nil {
		return 0
	}
	count := 0
	for _, record := range records {
		if record.Kind == kindProgress {
			count++
		}
	}
	return count
}
