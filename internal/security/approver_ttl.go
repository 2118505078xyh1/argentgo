package security

import (
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
	"unicode"
	"unicode/utf8"
)

const (
	// defaultTTLGrantLifetime is used when the caller passes a non-positive ttl.
	defaultTTLGrantLifetime = 30 * time.Minute
	// maxTTLGrantEntries bounds both the in-memory and the on-disk grant set.
	maxTTLGrantEntries = 1000
	// maxTTLCanonicalBytes bounds how much argument text Key inspects.
	maxTTLCanonicalBytes = 64 * 1024
	// maxTTLReadableRunes bounds the human-readable context kept per grant.
	maxTTLReadableRunes = 200
	// ttlGrantsFile is the persistence file created inside dir.
	ttlGrantsFile = "grants.json"
)

// ttlEntry is one persisted grant. ExpiresAt is absolute wall-clock time so a
// grant written by a previous process keeps the same remaining lifetime after
// a restart. Readable is redacted and rune-capped before it is written, so the
// file never holds a credential.
type ttlEntry struct {
	Key       string    `json:"key"`
	Readable  string    `json:"readable"`
	ExpiresAt time.Time `json:"expires_at"`
}

// TTLCache is a self-contained approval-grant cache with an expiry and
// optional JSON persistence. Unlike CachedApprover it expires grants and can
// survive a restart when a directory is supplied. Denials are never recorded.
//
// Grants are stored as a JSON array in <dir>/grants.json, written atomically
// (temp file + rename, mode 0600). Expired grants are dropped on read and on
// write, and the set is capped at maxTTLGrantEntries. An empty dir makes the
// cache memory-only.
type TTLCache struct {
	mu      sync.Mutex
	dir     string
	ttl     time.Duration
	entries map[string]ttlEntry
	// now is the expiry clock. Tests replace it with a fixed clock; it is set
	// once at construction and never mutated afterwards.
	now func() time.Time
}

// NewTTLCache creates a cache with ttl (default 30 minutes when non-positive).
// Persisted grants are loaded best-effort: a missing, unreadable or corrupt
// file leaves an empty cache rather than failing construction.
func NewTTLCache(dir string, ttl time.Duration) *TTLCache {
	if ttl <= 0 {
		ttl = defaultTTLGrantLifetime
	}
	c := &TTLCache{
		dir:     dir,
		ttl:     ttl,
		entries: make(map[string]ttlEntry),
		now:     time.Now,
	}
	if dir != "" {
		_ = c.Load()
	}
	return c
}

// Key returns the stable hex SHA-256 identity of a (tool, args) grant. It
// mirrors approvalCacheKey so a CachedApprover caller can reuse the same key:
// sha256(tool + "\x00" + canonicalArgs(args)).
func (c *TTLCache) Key(tool string, args json.RawMessage) string {
	sum := sha256.Sum256([]byte(tool + "\x00" + canonicalTTLArgs(args)))
	return hex.EncodeToString(sum[:])
}

// canonicalTTLArgs mirrors approver.go's canonicalArgs: object keys are sorted
// recursively by json.Marshal, UseNumber keeps numeric spellings such as 1 and
// 1.0 distinct, and trailing data falls back to whitespace-collapsed text.
// Input is bounded to 64 KiB; oversized arguments get a length-tagged prefix
// so two long argument sets cannot collide on one key.
func canonicalTTLArgs(raw json.RawMessage) string {
	if len(raw) > maxTTLCanonicalBytes {
		return fmt.Sprintf("oversized:%d:%s", len(raw), string(raw[:maxTTLCanonicalBytes]))
	}
	s := strings.TrimSpace(string(raw))
	if s == "" {
		return "{}"
	}
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return strings.Join(strings.Fields(s), " ")
	}
	// Reject trailing data so a value with a suffix cannot share the key of
	// the bare value, matching json.Unmarshal in canonicalArgs.
	if err := dec.Decode(new(any)); err != io.EOF {
		return strings.Join(strings.Fields(s), " ")
	}
	normalized, err := json.Marshal(value)
	if err != nil {
		return s
	}
	return string(normalized)
}

// Allow records a grant for key that lives until now+ttl. readable is
// sanitized, redacted and capped before it is stored or written to disk. A
// persistence failure is returned, but the in-memory grant still applies for
// the current process.
func (c *TTLCache) Allow(key, readable string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	c.purgeExpiredLocked(now)
	c.entries[key] = ttlEntry{
		Key:       key,
		Readable:  ttlSafeContext(readable),
		ExpiresAt: now.Add(c.ttl),
	}
	c.enforceCapLocked()
	return c.saveLocked()
}

// IsAllowed reports whether key has an unexpired grant. An expired grant is
// removed and, when persisted, rewritten to disk before returning false.
func (c *TTLCache) IsAllowed(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return false
	}
	if !c.now().Before(entry.ExpiresAt) {
		delete(c.entries, key)
		if c.dir != "" {
			_ = c.saveLocked()
		}
		return false
	}
	return true
}

// Deny removes any grant for key. Denials are never persisted: the remaining
// grants are rewritten, but no "denied" record is ever created.
func (c *TTLCache) Deny(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.entries[key]; !ok {
		return
	}
	delete(c.entries, key)
	if c.dir != "" {
		_ = c.saveLocked()
	}
}

// Load reads <dir>/grants.json and keeps only unexpired grants; expired ones
// are purged from both memory and the file. It is a no-op for a memory-only
// cache and treats a missing file as success.
func (c *TTLCache) Load() error {
	if c.dir == "" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	data, err := os.ReadFile(filepath.Join(c.dir, ttlGrantsFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("security: read ttl grants: %w", err)
	}
	var loaded []ttlEntry
	if err := json.Unmarshal(data, &loaded); err != nil {
		return fmt.Errorf("security: decode ttl grants: %w", err)
	}
	now := c.now()
	// Purge entries loaded earlier too: a second Load with a later clock must
	// not write a stale in-memory grant back to the file.
	purged := c.purgeExpiredLocked(now)
	for _, e := range loaded {
		if e.Key == "" || !now.Before(e.ExpiresAt) {
			purged = true
			continue
		}
		// Re-sanitize on load so a hand-edited file cannot smuggle a secret in.
		e.Readable = ttlSafeContext(e.Readable)
		c.entries[e.Key] = e
	}
	if purged || c.enforceCapLocked() {
		return c.saveLocked()
	}
	return nil
}

// purgeExpiredLocked drops grants whose expiry is not in the future. The
// caller holds mu.
func (c *TTLCache) purgeExpiredLocked(now time.Time) bool {
	removed := false
	for key, e := range c.entries {
		if !now.Before(e.ExpiresAt) {
			delete(c.entries, key)
			removed = true
		}
	}
	return removed
}

// enforceCapLocked evicts the soonest-to-expire grants when the set grows past
// maxTTLGrantEntries. Ordering by expiry keeps eviction deterministic and
// drops the least-trusted grants first. The caller holds mu.
func (c *TTLCache) enforceCapLocked() bool {
	if len(c.entries) <= maxTTLGrantEntries {
		return false
	}
	type expiring struct {
		key string
		at  time.Time
	}
	all := make([]expiring, 0, len(c.entries))
	for key, e := range c.entries {
		all = append(all, expiring{key: key, at: e.ExpiresAt})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].at.Equal(all[j].at) {
			return all[i].key < all[j].key
		}
		return all[i].at.Before(all[j].at)
	})
	for _, e := range all[:len(all)-maxTTLGrantEntries] {
		delete(c.entries, e.key)
	}
	return true
}

// saveLocked atomically writes the whole grant set as a sorted JSON array.
// The caller holds mu.
func (c *TTLCache) saveLocked() error {
	if c.dir == "" {
		return nil
	}
	entries := make([]ttlEntry, 0, len(c.entries))
	for _, e := range c.entries {
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })
	data, err := json.Marshal(entries)
	if err != nil {
		return fmt.Errorf("security: encode ttl grants: %w", err)
	}
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		return fmt.Errorf("security: create ttl grants dir: %w", err)
	}
	tmp, err := os.CreateTemp(c.dir, "grants-*.tmp")
	if err != nil {
		return fmt.Errorf("security: create ttl grants temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("security: chmod ttl grants temp: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("security: write ttl grants temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("security: close ttl grants temp: %w", err)
	}
	if err := os.Rename(tmpName, filepath.Join(c.dir, ttlGrantsFile)); err != nil {
		return fmt.Errorf("security: rename ttl grants: %w", err)
	}
	return nil
}

// ttlSafeContext strips control bytes, redacts credential-shaped spans and
// caps the result on a rune boundary so a persisted context can never carry a
// secret or an unbounded blob.
func ttlSafeContext(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			i++
			continue
		}
		if unicode.IsControl(r) {
			b.WriteByte(' ')
		} else {
			b.WriteRune(r)
		}
		i += size
	}
	return ttlRuneHead(Redact(b.String()), maxTTLReadableRunes)
}

// ttlRuneHead returns the first n runes of s without splitting a UTF-8
// sequence.
func ttlRuneHead(s string, n int) string {
	if n <= 0 {
		return ""
	}
	seen := 0
	for pos := range s {
		if seen == n {
			return s[:pos]
		}
		seen++
	}
	return s
}
