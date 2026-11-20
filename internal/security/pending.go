package security

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// PendingApproval is one tool call waiting for an operator decision. It is the
// durable, server-authoritative mirror of the in-memory approval modal: a
// reconnecting client can re-list pending requests from the store instead of
// losing them when the TUI exits. Raw arguments are never stored; ArgsDigest is
// a hash of their canonical form so two spellings of the same call compare
// equal without retaining possibly secret argument text.
type PendingApproval struct {
	ID         string `json:"id"`
	Session    string `json:"session"`
	Tool       string `json:"tool"`
	ArgsDigest string `json:"args_digest"`
	Created    string `json:"created"`
	Reason     string `json:"reason,omitempty"`
}

// Bounds keep a hostile or buggy client from filling the store with unbounded
// records. They are deliberately generous for normal tool calls.
const (
	maxPendingPerSession  = 50
	maxPendingDecisionLog = 200
	maxPendingIDLen       = 256
	maxPendingSessionLen  = 512
	maxPendingToolLen     = 256
	maxPendingDigestLen   = 128
	maxPendingCreatedLen  = 64
	maxPendingReasonLen   = 4096
	maxCanonicalArgsBytes = 64 * 1024
	maxPendingLineBytes   = 2 * 1024 * 1024
)

// PendingStore persists pending approvals on disk. All operations are guarded
// by a mutex so a server handling concurrent HTTP/SSE clients can share one
// store safely. The zero value is not usable; use NewPendingStore.
type PendingStore struct {
	dir string
	mu  sync.Mutex
}

// NewPendingStore returns a store rooted at dir, creating the directory. When
// dir is empty it defaults to ".argent/pending" relative to the working
// directory. The caller is expected to pass a base path; the store appends its
// own subdirectory only via that default.
func NewPendingStore(dir string) *PendingStore {
	if strings.TrimSpace(dir) == "" {
		dir = filepath.Join(".argent", "pending")
	}
	s := &PendingStore{dir: dir}
	// The constructor cannot return an error; each method retries MkdirAll so
	// a transient filesystem failure here is not fatal.
	_ = os.MkdirAll(dir, 0o700)
	return s
}

// PendingArgsDigest returns the sha256 of the canonical form of raw arguments.
// Callers put this value in PendingApproval.ArgsDigest; raw args are never
// written to disk. Canonicalization mirrors security.canonicalArgs: JSON object
// keys are sorted and numbers stay exact, non-JSON text is whitespace-collapsed.
func PendingArgsDigest(raw string) string {
	sum := sha256.Sum256([]byte(canonicalPendingArgs(raw)))
	return hex.EncodeToString(sum[:])
}

// canonicalPendingArgs canonicalizes raw arguments with encoding/json so two
// spellings of the same call hash identically. It is bounded: oversized input
// is truncated with a total-length marker so distinct long blobs cannot share
// one digest and the decoder never has to walk an unbounded string.
func canonicalPendingArgs(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "{}"
	}
	if len(raw) > maxCanonicalArgsBytes {
		raw = raw[:maxCanonicalArgsBytes] + fmt.Sprintf("\x00len=%d", len(raw))
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return strings.Join(strings.Fields(raw), " ")
	}
	// Reject trailing data, as json.Unmarshal does, so a value with a suffix
	// cannot collapse onto the digest of the bare value.
	if err := dec.Decode(new(any)); err != io.EOF {
		return strings.Join(strings.Fields(raw), " ")
	}
	normalized, err := json.Marshal(value)
	if err != nil {
		return raw
	}
	return string(normalized)
}

// Add stores p for its session, replacing any request with the same ID. It
// rewrites the whole per-session file atomically (temp file + rename) and keeps
// at most maxPendingPerSession records, dropping the oldest on overflow.
func (s *PendingStore) Add(p PendingApproval) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureDir(); err != nil {
		return err
	}
	// Hash the raw session for the path so the filename never contains a
	// session id (or any path-separated characters from it).
	path := s.sessionPath(p.Session)
	clean := sanitizePending(p)
	items, err := s.readPending(path)
	if err != nil {
		return err
	}
	kept := make([]PendingApproval, 0, len(items)+1)
	for _, item := range items {
		if item.ID == clean.ID {
			continue
		}
		kept = append(kept, item)
	}
	kept = append(kept, clean)
	if len(kept) > maxPendingPerSession {
		kept = kept[len(kept)-maxPendingPerSession:]
	}
	return s.writePending(path, kept)
}

// List returns the pending requests for session, sorted by Created ascending.
// A missing session yields an empty (non-nil) slice. Malformed lines are
// skipped so a partially written or hand-edited file cannot break reconnects.
func (s *PendingStore) List(session string) ([]PendingApproval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items, err := s.readPending(s.sessionPath(session))
	if err != nil {
		return nil, err
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Created < items[j].Created })
	if items == nil {
		items = []PendingApproval{}
	}
	return items, nil
}

// Resolve removes the request with the given id and appends a bounded one-line
// decision record. An unknown id is a no-op: reconnect races may replay a
// decision after another client already resolved it, and failing that would
// surface a spurious error to the operator.
func (s *PendingStore) Resolve(session, id string, granted bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureDir(); err != nil {
		return err
	}
	path := s.sessionPath(session)
	items, err := s.readPending(path)
	if err != nil {
		return err
	}
	cleanID := truncatePendingRunes(Redact(id), maxPendingIDLen)
	kept := make([]PendingApproval, 0, len(items))
	found := false
	for _, item := range items {
		if item.ID == cleanID {
			found = true
			continue
		}
		kept = append(kept, item)
	}
	if !found {
		return nil
	}
	if err := s.writePending(path, kept); err != nil {
		return err
	}
	return s.appendDecision(session, cleanID, granted)
}

// Clear removes every pending request for session. Decision history is kept;
// it is an audit trail, not pending state.
func (s *PendingStore) Clear(session string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.sessionPath(session)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("security: clear pending store: %w", err)
	}
	return nil
}

// ensureDir creates the backing directory with owner-only permissions.
func (s *PendingStore) ensureDir() error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("security: create pending store dir: %w", err)
	}
	return nil
}

// sessionHash is a filesystem-safe, non-reversible name for a session id.
func sessionHash(session string) string {
	sum := sha256.Sum256([]byte(session))
	return hex.EncodeToString(sum[:])
}

func (s *PendingStore) sessionPath(session string) string {
	return filepath.Join(s.dir, sessionHash(session)+".json")
}

func (s *PendingStore) decisionsPath(session string) string {
	return filepath.Join(s.dir, sessionHash(session)+".decisions.jsonl")
}

// sanitizePending redacts every string field before persistence and caps each
// one, so secrets and oversized blobs never reach disk. A missing Created is
// filled in so List's sort has a stable key.
func sanitizePending(p PendingApproval) PendingApproval {
	p.ID = truncatePendingRunes(Redact(p.ID), maxPendingIDLen)
	p.Session = truncatePendingRunes(Redact(p.Session), maxPendingSessionLen)
	p.Tool = truncatePendingRunes(Redact(p.Tool), maxPendingToolLen)
	p.ArgsDigest = truncatePendingRunes(p.ArgsDigest, maxPendingDigestLen)
	p.Created = truncatePendingRunes(p.Created, maxPendingCreatedLen)
	p.Reason = truncatePendingRunes(Redact(p.Reason), maxPendingReasonLen)
	if p.Created == "" {
		p.Created = time.Now().UTC().Format(time.RFC3339Nano)
	}
	return p
}

// truncatePendingRunes caps s at max runes, never cutting a UTF-8 sequence.
func truncatePendingRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	n := 0
	for i := range s {
		if n == max {
			return s[:i]
		}
		n++
	}
	return s
}

// readPending reads newline-delimited PendingApproval records, skipping blank
// and malformed lines. A missing file is an empty store, not an error.
func (s *PendingStore) readPending(path string) ([]PendingApproval, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("security: open pending store: %w", err)
	}
	defer f.Close()
	var out []PendingApproval
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxPendingLineBytes)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var p PendingApproval
		if err := json.Unmarshal(line, &p); err != nil {
			continue
		}
		if p.ID == "" {
			continue
		}
		out = append(out, p)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("security: scan pending store: %w", err)
	}
	return out, nil
}

// writePending rewrites path as newline-delimited JSON via a temp file and
// rename, matching the config writer's crash-safe pattern.
func (s *PendingStore) writePending(path string, items []PendingApproval) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, item := range items {
		if err := enc.Encode(item); err != nil {
			return fmt.Errorf("security: encode pending store: %w", err)
		}
	}
	return atomicWriteFile(path, buf.Bytes())
}

// atomicWriteFile writes data to path with owner-only permissions. A temp file
// in the same directory is renamed into place so readers never see a partial
// write and a crash cannot corrupt the previous content.
func atomicWriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("security: create pending store dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".pending-*")
	if err != nil {
		return fmt.Errorf("security: create pending temp file: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("security: chmod pending temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("security: write pending temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("security: close pending temp file: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("security: rename pending temp file: %w", err)
	}
	return nil
}

// pendingDecision is one resolved decision appended to the JSONL history.
type pendingDecision struct {
	ID      string `json:"id"`
	Granted bool   `json:"granted"`
	TS      string `json:"ts"`
}

// appendDecision records a decision, then trims the log to its last
// maxPendingDecisionLog lines so the audit trail stays bounded.
func (s *PendingStore) appendDecision(session, id string, granted bool) error {
	rec := pendingDecision{
		ID:      id,
		Granted: granted,
		TS:      time.Now().UTC().Format(time.RFC3339Nano),
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("security: encode pending decision: %w", err)
	}
	path := s.decisionsPath(session)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("security: open pending decisions: %w", err)
	}
	_, writeErr := f.Write(append(line, '\n'))
	closeErr := f.Close()
	if writeErr != nil {
		return fmt.Errorf("security: append pending decision: %w", writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("security: close pending decisions: %w", closeErr)
	}
	return s.boundDecisions(path)
}

// boundDecisions keeps only the newest decision lines when the log overflows.
func (s *PendingStore) boundDecisions(path string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("security: read pending decisions: %w", err)
	}
	lines := bytes.Split(data, []byte("\n"))
	valid := lines[:0]
	for _, line := range lines {
		if len(bytes.TrimSpace(line)) > 0 {
			valid = append(valid, line)
		}
	}
	if len(valid) <= maxPendingDecisionLog {
		return nil
	}
	kept := valid[len(valid)-maxPendingDecisionLog:]
	out := bytes.Join(kept, []byte("\n"))
	out = append(out, '\n')
	return atomicWriteFile(path, out)
}
