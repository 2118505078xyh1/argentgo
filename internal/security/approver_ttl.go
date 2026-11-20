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
	// ttlGrantsLockFile is a sidecar used for a cross-process lock. The OS owns
	// the lock and drops it on process exit, so it never goes stale.
	ttlGrantsLockFile = "grants.json.lock"
)

// errTTLUntrusted is returned by Allow when the persisted grant set could not
// be read or decoded. The cache refuses to grant or to overwrite a file it
// cannot trust; an explicit successful Load is required to recover.
var errTTLUntrusted = errors.New("security: ttl grants untrusted")

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
// (temp file + sync + rename, mode 0600). Expired grants are dropped on read
// and on write, and the set is capped at maxTTLGrantEntries. An empty dir makes
// the cache memory-only.
//
// When persisted, every write takes a cross-process lock on the sidecar
// <dir>/grants.json.lock,
// re-reads the latest grant set from disk and only then applies the change, so
// two processes cannot clobber each other's grants. A file that cannot be read
// or decoded fails closed: IsAllowed returns false and Allow returns an error
// instead of silently starting from an empty (or fresh) grant set.
type TTLCache struct {
	mu      sync.Mutex
	dir     string
	ttl     time.Duration
	entries map[string]ttlEntry
	// now is the expiry clock. Tests replace it with a fixed clock; it is set
	// once at construction and never mutated afterwards.
	now func() time.Time
	// failed is set when the persisted grant file cannot be trusted. While set,
	// no grant is honoured and no write is attempted. It is cleared by a
	// successful Load (or a write whose re-read from disk succeeded).
	failed bool
}

// NewTTLCache creates a cache with ttl (default 30 minutes when non-positive).
// Persisted grants are loaded from disk; a missing file is fine, but a read or
// decode failure marks the cache untrusted so it fails closed rather than
// treating the corrupt file as an empty grant set.
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
		if err := c.Load(); err != nil {
			c.failed = true
		}
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
// sanitized, redacted and capped before it is stored or written to disk. For a
// persisted cache the on-disk set is re-read under the cross-process lock
// before the grant is added, so a concurrent writer's grants are preserved. A
// persistence write failure is returned but the in-memory grant still applies
// for the current process; an untrusted grant file fails closed instead.
func (c *TTLCache) Allow(key, readable string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failed {
		return errTTLUntrusted
	}
	now := c.now()
	if c.dir == "" {
		purgeGrantEntries(c.entries, now)
		c.entries[key] = newTTLGrant(key, readable, now.Add(c.ttl))
		capGrantEntries(c.entries)
		return nil
	}
	return c.withGrantLock(func() error {
		entries, _, err := c.readGrantsLocked()
		if err != nil {
			c.failed = true
			return err
		}
		purgeGrantEntries(entries, now)
		entries[key] = newTTLGrant(key, readable, now.Add(c.ttl))
		capGrantEntries(entries)
		// Refresh the in-memory view before writing: the grant is live in this
		// process even if the write itself fails.
		c.entries = entries
		return c.writeGrantsLocked(entries)
	})
}

// IsAllowed reports whether key has an unexpired grant. An expired grant is
// removed and, when persisted, rewritten to disk before returning false. When
// the cache is untrusted it always reports false.
func (c *TTLCache) IsAllowed(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failed {
		return false
	}
	entry, ok := c.entries[key]
	if !ok {
		return false
	}
	if !c.now().Before(entry.ExpiresAt) {
		delete(c.entries, key)
		if c.dir != "" {
			_ = c.withGrantLock(func() error {
				entries, _, err := c.readGrantsLocked()
				if err != nil {
					c.failed = true
					return err
				}
				delete(entries, key)
				c.entries = entries
				return c.writeGrantsLocked(entries)
			})
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
	if c.dir == "" {
		delete(c.entries, key)
		return
	}
	if c.failed {
		return
	}
	_ = c.withGrantLock(func() error {
		entries, _, err := c.readGrantsLocked()
		if err != nil {
			c.failed = true
			return err
		}
		if _, ok := entries[key]; !ok {
			c.entries = entries
			return nil
		}
		delete(entries, key)
		c.entries = entries
		return c.writeGrantsLocked(entries)
	})
}

// Load reads <dir>/grants.json and keeps only unexpired grants; expired ones
// are purged from both memory and the file. It is a no-op for a memory-only
// cache and treats a missing file as success. A read/decode failure returns an
// error and marks the cache untrusted (fail-closed); a later successful Load
// clears that state.
func (c *TTLCache) Load() error {
	if c.dir == "" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.withGrantLock(func() error {
		entries, dirty, err := c.readGrantsLocked()
		if err != nil {
			c.failed = true
			return err
		}
		c.entries = entries
		c.failed = false
		if dirty {
			return c.writeGrantsLocked(entries)
		}
		return nil
	})
}

// withGrantLock runs fn while holding the cross-process lock on the sidecar
// file. The caller must hold c.mu. It is a no-op for a memory-only cache.
func (c *TTLCache) withGrantLock(fn func() error) error {
	if c.dir == "" {
		return fn()
	}
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		return fmt.Errorf("security: create ttl grants dir: %w", err)
	}
	lk, err := ttlAcquireLock(filepath.Join(c.dir, ttlGrantsLockFile))
	if err != nil {
		return err
	}
	defer lk.Release()
	return fn()
}

// readGrantsLocked reads the persisted grant set and returns a fresh map with
// expired entries dropped and readable context re-sanitized. dirty reports
// whether the file representation differs from the returned map and therefore
// ought to be rewritten. A missing file is an empty, clean set; a read or
// decode error is returned so callers can fail closed. The caller holds the
// cross-process lock and c.mu.
func (c *TTLCache) readGrantsLocked() (map[string]ttlEntry, bool, error) {
	data, err := os.ReadFile(filepath.Join(c.dir, ttlGrantsFile))
	if errors.Is(err, os.ErrNotExist) {
		return make(map[string]ttlEntry), false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("security: read ttl grants: %w", err)
	}
	var loaded []ttlEntry
	if err := json.Unmarshal(data, &loaded); err != nil {
		return nil, false, fmt.Errorf("security: decode ttl grants: %w", err)
	}
	now := c.now()
	entries := make(map[string]ttlEntry, len(loaded))
	dirty := false
	for _, e := range loaded {
		if e.Key == "" || !now.Before(e.ExpiresAt) {
			dirty = true
			continue
		}
		// Re-sanitize on load so a hand-edited file cannot smuggle a secret in.
		clean := ttlSafeContext(e.Readable)
		if clean != e.Readable {
			dirty = true
		}
		e.Readable = clean
		entries[e.Key] = e
	}
	if capGrantEntries(entries) {
		dirty = true
	}
	return entries, dirty, nil
}

// writeGrantsLocked atomically writes the whole grant set as a sorted JSON
// array. The caller holds the cross-process lock and c.mu.
func (c *TTLCache) writeGrantsLocked(entries map[string]ttlEntry) error {
	list := make([]ttlEntry, 0, len(entries))
	for _, e := range entries {
		list = append(list, e)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Key < list[j].Key })
	data, err := json.Marshal(list)
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
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("security: sync ttl grants temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("security: close ttl grants temp: %w", err)
	}
	if err := os.Rename(tmpName, filepath.Join(c.dir, ttlGrantsFile)); err != nil {
		return fmt.Errorf("security: rename ttl grants: %w", err)
	}
	return nil
}

// newTTLGrant builds a grant entry with sanitized, bounded readable context.
func newTTLGrant(key, readable string, expires time.Time) ttlEntry {
	return ttlEntry{Key: key, Readable: ttlSafeContext(readable), ExpiresAt: expires}
}

// purgeGrantEntries drops grants whose expiry is not in the future.
func purgeGrantEntries(entries map[string]ttlEntry, now time.Time) bool {
	removed := false
	for key, e := range entries {
		if !now.Before(e.ExpiresAt) {
			delete(entries, key)
			removed = true
		}
	}
	return removed
}

// capGrantEntries evicts the soonest-to-expire grants when the set grows past
// maxTTLGrantEntries. Ordering by expiry keeps eviction deterministic and
// drops the least-trusted grants first.
func capGrantEntries(entries map[string]ttlEntry) bool {
	if len(entries) <= maxTTLGrantEntries {
		return false
	}
	type expiring struct {
		key string
		at  time.Time
	}
	all := make([]expiring, 0, len(entries))
	for key, e := range entries {
		all = append(all, expiring{key: key, at: e.ExpiresAt})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].at.Equal(all[j].at) {
			return all[i].key < all[j].key
		}
		return all[i].at.Before(all[j].at)
	})
	for _, e := range all[:len(all)-maxTTLGrantEntries] {
		delete(entries, e.key)
	}
	return true
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
