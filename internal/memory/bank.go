package memory

// Persistent fact store. Unlike the instruction files in memory.go (which are
// project-owned, immutable input to the system prompt), this bank is written by
// the agent itself and survives across sessions. It is a single JSONL log:
// one Memory per line, appended and synced per write, rewritten through a
// temporary file for cap enforcement and deletion.

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

const (
	// memoryFileName is the append-only log backing a Bank.
	memoryFileName = "memory.jsonl"
	// defaultCap bounds retained entries: 2000 * 4KB is roughly 8MB on disk.
	defaultCap = 2000
	// maxTextBytes bounds the stored text of one entry (and of source/tags).
	maxTextBytes = 4096
	// maxTagBytes bounds a single tag so a malicious model cannot pad the log.
	maxTagBytes = 64
	// defaultRecallLimit applies when a caller passes a non-positive limit.
	defaultRecallLimit = 10
	// tagMatchBonus outweighs a single token hit so explicit tags act as a
	// strong filter rather than a faint relevance hint.
	tagMatchBonus = 3
	// scannerBufferSize accommodates a 4KB text plus tags and metadata.
	scannerBufferSize = 1024 * 1024
)

// ErrEmptyText is returned when Retain is asked to store nothing.
var ErrEmptyText = errors.New("memory: text must not be empty")

// Memory is one durable fact. TS is stored in UTC.
type Memory struct {
	ID     string    `json:"id"`
	Text   string    `json:"text"`
	Tags   []string  `json:"tags,omitempty"`
	TS     time.Time `json:"ts"`
	Source string    `json:"source,omitempty"`
}

// Bank is a concurrency-safe persistent fact store backed by one JSONL file.
// Construct it with NewBank; the zero value is not usable.
type Bank struct {
	dir  string
	path string
	mu   sync.Mutex
	cap  int
}

// NewBank returns a bank rooted at dir, creating the directory when possible.
// An empty dir falls back to a private directory under os.TempDir().
func NewBank(dir string) *Bank {
	if strings.TrimSpace(dir) == "" {
		dir = filepath.Join(os.TempDir(), "argent-memory")
	}
	_ = os.MkdirAll(dir, 0o755)
	return &Bank{dir: dir, path: filepath.Join(dir, memoryFileName), cap: defaultCap}
}

// SetDir repoints the bank at a new directory. It is intended for setup, before
// concurrent use; the file itself is created lazily on the next write.
func (b *Bank) SetDir(dir string) {
	if strings.TrimSpace(dir) == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.dir = dir
	b.path = filepath.Join(dir, memoryFileName)
	_ = os.MkdirAll(dir, 0o755)
}

// Dir reports the directory currently backing the bank.
func (b *Bank) Dir() string { return b.dir }

// Retain stores a fact and returns its generated id. The text, tags and source
// are redacted with security.Redact before anything reaches disk, so the memory
// file never contains credentials. Stores over the cap drop the oldest entries.
func (b *Bank) Retain(text string, tags []string, source string) (string, error) {
	body := boundedText(text)
	if body == "" {
		return "", ErrEmptyText
	}
	id, err := newID()
	if err != nil {
		return "", err
	}
	mem := Memory{
		ID:     id,
		Text:   body,
		Tags:   cleanTags(tags),
		TS:     time.Now().UTC(),
		Source: boundedText(source),
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if err := b.appendLocked(mem); err != nil {
		return "", err
	}
	loaded, err := b.loadLocked()
	if err != nil {
		return "", err
	}
	if b.cap > 0 && len(loaded) > b.cap {
		// Keep the tail: the log is in insertion order, so the last cap entries
		// are the newest ones regardless of clock resolution.
		if err := b.writeAllLocked(loaded[len(loaded)-b.cap:]); err != nil {
			return "", err
		}
	}
	return id, nil
}

// Recall loads the bank and returns up to limit memories ranked by token
// overlap with query plus tag matches, ties broken newest-first. A non-positive
// limit uses defaultRecallLimit. With an empty query and no tags every memory
// is eligible, so the result is simply the newest first. Malformed lines are
// skipped leniently.
func (b *Bank) Recall(query string, tags []string, limit int) []Memory {
	b.mu.Lock()
	all, err := b.loadLocked()
	b.mu.Unlock()
	if len(all) == 0 {
		return nil
	}
	_ = err // partial results are still useful; a truncated tail is ignored

	if limit <= 0 {
		limit = defaultRecallLimit
	}
	queryTokens := tokenize(query)
	wantTags := cleanTags(tags)
	filtered := len(queryTokens) > 0 || len(wantTags) > 0

	type hit struct {
		mem   Memory
		score int
		idx   int // file order, used as a stable newest-first tie breaker
	}
	results := make([]hit, 0, len(all))
	for i, mem := range all {
		score := 0
		if len(queryTokens) > 0 {
			textTokens := tokenize(mem.Text)
			for _, q := range queryTokens {
				for _, tok := range textTokens {
					if q == tok {
						score++
						break
					}
				}
			}
		}
		if len(wantTags) > 0 {
			for _, want := range wantTags {
				for _, tag := range mem.Tags {
					if strings.EqualFold(want, tag) {
						score += tagMatchBonus
						break
					}
				}
			}
		}
		if filtered && score == 0 {
			continue
		}
		results = append(results, hit{mem: mem, score: score, idx: i})
	}

	sort.SliceStable(results, func(i, j int) bool {
		if results[i].score != results[j].score {
			return results[i].score > results[j].score
		}
		if results[i].mem.TS.Equal(results[j].mem.TS) {
			return results[i].idx > results[j].idx
		}
		return results[i].mem.TS.After(results[j].mem.TS)
	})
	if len(results) > limit {
		results = results[:limit]
	}
	out := make([]Memory, len(results))
	for i, r := range results {
		out[i] = r.mem
	}
	return out
}

// Forget removes the memory with id. It is idempotent: an unknown id is a no-op.
func (b *Bank) Forget(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("memory: id must not be empty")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	all, err := b.loadLocked()
	if err != nil {
		return err
	}
	kept := all[:0]
	removed := false
	for _, mem := range all {
		if mem.ID == id {
			removed = true
			continue
		}
		kept = append(kept, mem)
	}
	if !removed {
		return nil
	}
	return b.writeAllLocked(kept)
}

// All returns every valid memory in insertion order, skipping malformed lines.
func (b *Bank) All() []Memory {
	b.mu.Lock()
	defer b.mu.Unlock()
	all, _ := b.loadLocked()
	if all == nil {
		return []Memory{}
	}
	return all
}

// appendLocked writes one record with a single write + Sync so a crash cannot
// leave a partial line in the log. The caller holds b.mu.
func (b *Bank) appendLocked(mem Memory) error {
	line, err := json.Marshal(mem)
	if err != nil {
		return fmt.Errorf("memory: encode: %w", err)
	}
	line = append(line, '\n')
	f, err := os.OpenFile(b.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("memory: open: %w", err)
	}
	if _, err := f.Write(line); err != nil {
		_ = f.Close()
		return fmt.Errorf("memory: append: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("memory: sync: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("memory: close: %w", err)
	}
	return nil
}

// loadLocked reads the whole log. Bad lines are skipped rather than failing the
// whole read, so one corrupt record cannot hide the rest. The caller holds b.mu.
func (b *Bank) loadLocked() ([]Memory, error) {
	f, err := os.Open(b.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("memory: open: %w", err)
	}
	defer f.Close()

	var out []Memory
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), scannerBufferSize)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var mem Memory
		if err := json.Unmarshal([]byte(line), &mem); err != nil {
			continue
		}
		if mem.ID == "" || mem.Text == "" {
			continue
		}
		out = append(out, mem)
	}
	if err := scanner.Err(); err != nil {
		return out, fmt.Errorf("memory: scan: %w", err)
	}
	return out, nil
}

// writeAllLocked replaces the log atomically from a temporary file in the same
// directory. Windows may refuse to rename over an existing file, so that case
// falls back to removing the target first. The caller holds b.mu.
func (b *Bank) writeAllLocked(mems []Memory) error {
	tmp, err := os.CreateTemp(b.dir, "memory-*.jsonl.tmp")
	if err != nil {
		return fmt.Errorf("memory: temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	bw := bufio.NewWriter(tmp)
	for _, mem := range mems {
		line, err := json.Marshal(mem)
		if err != nil {
			_ = tmp.Close()
			return fmt.Errorf("memory: encode: %w", err)
		}
		if _, err := bw.Write(line); err != nil {
			_ = tmp.Close()
			return fmt.Errorf("memory: write: %w", err)
		}
		if err := bw.WriteByte('\n'); err != nil {
			_ = tmp.Close()
			return fmt.Errorf("memory: write: %w", err)
		}
	}
	if err := bw.Flush(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("memory: flush: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("memory: sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("memory: close: %w", err)
	}

	if err := os.Rename(tmpName, b.path); err != nil {
		if rmErr := os.Remove(b.path); rmErr != nil && !os.IsNotExist(rmErr) {
			return fmt.Errorf("memory: replace: %w", err)
		}
		if err2 := os.Rename(tmpName, b.path); err2 != nil {
			return fmt.Errorf("memory: replace: %w", err2)
		}
	}
	return nil
}

// boundedText trims, redacts and caps a stored string. Redaction happens before
// truncation so a clipped credential can never leave a fragment on disk.
func boundedText(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = security.Redact(s)
	if len(s) > maxTextBytes {
		s = clipRunes(s, maxTextBytes)
	}
	return strings.TrimSpace(s)
}

// cleanTags normalises, redacts and deduplicates tags.
func cleanTags(tags []string) []string {
	out := make([]string, 0, len(tags))
	seen := make(map[string]bool, len(tags))
	for _, tag := range tags {
		tag = boundedText(tag)
		if tag == "" {
			continue
		}
		if len(tag) > maxTagBytes {
			tag = clipRunes(tag, maxTagBytes)
		}
		key := strings.ToLower(tag)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, tag)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// tokenize lowercases text into trimmed words. Punctuation is removed so
// "quick," and "quick" match.
func tokenize(s string) []string {
	fields := strings.Fields(strings.ToLower(s))
	tokens := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.Trim(field, ".,;:!?\"'()[]{}<>")
		if field != "" {
			tokens = append(tokens, field)
		}
	}
	return tokens
}

// newID combines a nanosecond timestamp with random bytes so ids stay unique
// even when many facts are retained in the same tick.
func newID() (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("memory: generate id: %w", err)
	}
	return fmt.Sprintf("%d-%s", time.Now().UTC().UnixNano(), hex.EncodeToString(raw[:])), nil
}
