package security

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// AuditEntry is one audited action. Field names are the JSONL keys.
type AuditEntry struct {
	// TS is the RFC 3339 UTC timestamp; filled in by Record when empty.
	TS string `json:"ts"`
	// Tool is the tool name that was checked.
	Tool string `json:"tool"`
	// Decision is allow, ask, deny, refused, unavailable or error. "unavailable"
	// means no operator could answer, as opposed to "refused" (an explicit no).
	Decision string `json:"decision"`
	// Reason is the policy or approval explanation, already redacted.
	Reason string `json:"reason,omitempty"`
	// Args holds the redacted tool arguments.
	Args string `json:"args,omitempty"`
	// Session identifies the session that produced the call.
	Session string `json:"session,omitempty"`
	// PrivacyMode records the privacy posture in effect for this decision.
	// Empty preserves the pre-privacy behavior (the key is omitted entirely).
	PrivacyMode string `json:"privacy_mode,omitempty"`
}

// AuditLog appends redacted decisions to one JSONL file per UTC day under a
// directory. The log is evidence, not a gate: a write failure is returned but
// never blocks the run by itself.
type AuditLog struct {
	dir string
	now func() time.Time
	mu  sync.Mutex
}

// NewAuditLog opens (or prepares) a log writing `<dir>/<YYYY-MM-DD>.jsonl`.
// Callers normally pass `<storage>/audit`.
func NewAuditLog(dir string) *AuditLog {
	return &AuditLog{dir: dir, now: time.Now}
}

// Dir is the directory holding the daily files.
func (l *AuditLog) Dir() string { return l.dir }

// Path returns the file a record written now would land in.
func (l *AuditLog) Path() string {
	l.mu.Lock()
	now := l.now
	l.mu.Unlock()
	if now == nil {
		now = time.Now
	}
	return l.pathFor(now())
}

func (l *AuditLog) pathFor(t time.Time) string {
	return filepath.Join(l.dir, t.UTC().Format("2006-01-02")+".jsonl")
}

// Record appends one entry. Timestamp, reason and arguments are normalized
// and redacted here so no caller can bypass the scrubber.
func (l *AuditLog) Record(entry AuditEntry) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now
	if l.now != nil {
		now = l.now
	}
	stamp := now()
	if entry.TS == "" {
		entry.TS = stamp.UTC().Format(time.RFC3339)
	}
	entry.Reason = Redact(entry.Reason)
	if entry.Args != "" {
		entry.Args = RedactJSON([]byte(entry.Args))
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("security: encoding audit entry: %w", err)
	}
	if err := os.MkdirAll(l.dir, 0o700); err != nil {
		return fmt.Errorf("security: creating audit directory: %w", err)
	}
	file, err := os.OpenFile(l.pathFor(stamp), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("security: opening audit log: %w", err)
	}
	if _, err := file.Write(append(line, '\n')); err != nil {
		file.Close()
		return fmt.Errorf("security: writing audit log: %w", err)
	}
	return file.Close()
}

// Log is the convenience used by Chain: it redacts args and records one
// decision.
func (l *AuditLog) Log(session, tool string, decision Decision, reason string, args json.RawMessage) error {
	entry := AuditEntry{
		Tool:     tool,
		Decision: decision.String(),
		Reason:   reason,
		Session:  session,
	}
	if len(args) > 0 {
		entry.Args = RedactJSON(args)
	}
	return l.Record(entry)
}

// ReadAudit reads back the entries of one file, newest last.
func ReadAudit(path string) ([]AuditEntry, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()
	var entries []AuditEntry
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var entry AuditEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			return entries, fmt.Errorf("security: parsing audit line: %w", err)
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return entries, err
	}
	return entries, nil
}
