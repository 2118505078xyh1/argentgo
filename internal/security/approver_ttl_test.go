package security

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

// newTTLTestCache pins the expiry clock so tests never depend on wall time.
func newTTLTestCache(t *testing.T, dir string, ttl time.Duration, at time.Time) *TTLCache {
	t.Helper()
	c := NewTTLCache(dir, ttl)
	c.now = func() time.Time { return at }
	return c
}

func ttlGrantPath(dir string) string { return filepath.Join(dir, ttlGrantsFile) }

func TestTTLCacheAllowThenExpireRewritesFile(t *testing.T) {
	dir := t.TempDir()
	base := time.Now()
	const ttl = time.Hour
	c := newTTLTestCache(t, dir, ttl, base)

	key := c.Key("shell", json.RawMessage(`{"cmd":"echo hi"}`))
	if err := c.Allow(key, "echo hi"); err != nil {
		t.Fatalf("Allow: %v", err)
	}
	if !c.IsAllowed(key) {
		t.Fatal("grant should be allowed before expiry")
	}

	c.now = func() time.Time { return base.Add(ttl + time.Second) }
	if c.IsAllowed(key) {
		t.Fatal("grant should expire after ttl")
	}
	data, err := os.ReadFile(ttlGrantPath(dir))
	if err != nil {
		t.Fatalf("read grants file: %v", err)
	}
	if strings.Contains(string(data), key) {
		t.Fatalf("expired key should be purged from %s: %s", ttlGrantsFile, data)
	}
}

func TestTTLCachePersistsAcrossInstances(t *testing.T) {
	dir := t.TempDir()
	base := time.Now()
	const ttl = time.Hour

	c1 := newTTLTestCache(t, dir, ttl, base)
	key := c1.Key("shell", json.RawMessage(`{"cmd":"echo hi"}`))
	if err := c1.Allow(key, "echo hi"); err != nil {
		t.Fatalf("Allow: %v", err)
	}

	// The new instance loads from disk; its clock is pinned to the same base
	// so the grant is still inside its lifetime.
	c2 := NewTTLCache(dir, ttl)
	c2.now = func() time.Time { return base }
	if !c2.IsAllowed(key) {
		t.Fatal("grant should survive a new TTLCache instance")
	}
}

func TestTTLCacheLoadPurgesExpiredGrants(t *testing.T) {
	dir := t.TempDir()
	base := time.Now()
	const ttl = time.Hour

	c1 := newTTLTestCache(t, dir, ttl, base)
	key := c1.Key("shell", json.RawMessage(`{"cmd":"x"}`))
	if err := c1.Allow(key, "x"); err != nil {
		t.Fatalf("Allow: %v", err)
	}

	c2 := NewTTLCache(dir, ttl) // real clock: the grant is not expired yet
	c2.now = func() time.Time { return base.Add(ttl + time.Second) }
	if err := c2.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	data, err := os.ReadFile(ttlGrantPath(dir))
	if err != nil {
		t.Fatalf("read grants file: %v", err)
	}
	if strings.Contains(string(data), key) {
		t.Fatalf("Load should purge the expired key from disk: %s", data)
	}
	if c2.IsAllowed(key) {
		t.Fatal("expired grant must not be allowed after Load")
	}
}

func TestTTLCacheDenyRemovesGrantAndIsNotPersisted(t *testing.T) {
	dir := t.TempDir()
	base := time.Now()
	c := newTTLTestCache(t, dir, time.Hour, base)
	key := c.Key("shell", json.RawMessage(`{"cmd":"x"}`))
	if err := c.Allow(key, "x"); err != nil {
		t.Fatalf("Allow: %v", err)
	}

	c.Deny(key)
	if c.IsAllowed(key) {
		t.Fatal("denied grant must not be allowed")
	}
	data, err := os.ReadFile(ttlGrantPath(dir))
	if err != nil {
		t.Fatalf("read grants file: %v", err)
	}
	if strings.Contains(string(data), key) {
		t.Fatalf("deny must not leave a grant on disk: %s", data)
	}

	c2 := NewTTLCache(dir, time.Hour)
	c2.now = func() time.Time { return base }
	if c2.IsAllowed(key) {
		t.Fatal("denied grant must stay denied after reload")
	}
}

func TestTTLCacheRedactsReadableContextOnDisk(t *testing.T) {
	dir := t.TempDir()
	secret := "sk-PLANTED-SECRET-1234567890abcdef"
	c := newTTLTestCache(t, dir, time.Hour, time.Now())
	key := c.Key("shell", json.RawMessage(`{"cmd":"x"}`))
	readable := "allowed command but here is " + secret + " and more"
	if err := c.Allow(key, readable); err != nil {
		t.Fatalf("Allow: %v", err)
	}

	data, err := os.ReadFile(ttlGrantPath(dir))
	if err != nil {
		t.Fatalf("read grants file: %v", err)
	}
	if strings.Contains(string(data), secret) {
		t.Fatalf("secret leaked to disk: %s", data)
	}
	if !strings.Contains(string(data), "[redacted:api key]") {
		t.Fatalf("expected a redaction marker on disk: %s", data)
	}
}

func TestTTLCacheCapsReadableContextOnRuneBoundary(t *testing.T) {
	c := newTTLTestCache(t, "", time.Hour, time.Now())
	key := c.Key("shell", json.RawMessage(`{"a":1}`))
	long := strings.Repeat("界", maxTTLReadableRunes+50)
	if err := c.Allow(key, long); err != nil {
		t.Fatalf("Allow: %v", err)
	}

	c.mu.Lock()
	stored := c.entries[key].Readable
	c.mu.Unlock()
	if got := utf8.RuneCountInString(stored); got > maxTTLReadableRunes {
		t.Fatalf("readable context has %d runes, want <= %d", got, maxTTLReadableRunes)
	}
	if !utf8.ValidString(stored) {
		t.Fatal("readable context was cut inside a UTF-8 rune")
	}
}

func TestTTLCacheCapsEntryCount(t *testing.T) {
	c := newTTLTestCache(t, "", time.Hour, time.Now())
	for i := 0; i < maxTTLGrantEntries+5; i++ {
		args := json.RawMessage(`{"i":` + strconv.Itoa(i) + `}`)
		if err := c.Allow(c.Key("shell", args), "x"); err != nil {
			t.Fatalf("Allow #%d: %v", i, err)
		}
	}
	c.mu.Lock()
	n := len(c.entries)
	c.mu.Unlock()
	if n != maxTTLGrantEntries {
		t.Fatalf("cache holds %d entries, want %d", n, maxTTLGrantEntries)
	}
}

func TestTTLCacheMemoryOnly(t *testing.T) {
	c := newTTLTestCache(t, "", time.Hour, time.Now())
	key := c.Key("shell", json.RawMessage(`{"cmd":"x"}`))
	if err := c.Allow(key, "x"); err != nil {
		t.Fatalf("Allow: %v", err)
	}
	if !c.IsAllowed(key) {
		t.Fatal("memory-only grant should be allowed")
	}
	if err := c.Load(); err != nil {
		t.Fatalf("Load on a memory-only cache should be a no-op: %v", err)
	}
	if !c.IsAllowed(key) {
		t.Fatal("Load on a memory-only cache must not clear grants")
	}
}

func TestTTLCacheConcurrentUse(t *testing.T) {
	c := newTTLTestCache(t, "", time.Hour, time.Now())
	keys := []string{
		c.Key("a", json.RawMessage(`{"x":1}`)),
		c.Key("b", json.RawMessage(`{"x":2}`)),
	}
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				key := keys[(worker+i)%len(keys)]
				_ = c.Allow(key, "context")
				_ = c.IsAllowed(key)
				if i%5 == 0 {
					c.Deny(key)
				}
				_ = c.Key("a", json.RawMessage(`{"x":1}`))
			}
		}(worker)
	}
	wg.Wait()
}

func TestTTLCacheKeyCanonicalization(t *testing.T) {
	c := NewTTLCache("", 0)
	first := c.Key("shell", json.RawMessage(`{"a":1,"b":2}`))
	reordered := c.Key("shell", json.RawMessage(`{"b":2, "a":1}`))
	if first != reordered {
		t.Fatal("object key order must not affect the key")
	}
	decimal := c.Key("shell", json.RawMessage(`{"a":1.0,"b":2}`))
	if first == decimal {
		t.Fatal("1 and 1.0 must produce different keys")
	}
	otherTool := c.Key("other", json.RawMessage(`{"a":1,"b":2}`))
	if first == otherTool {
		t.Fatal("the tool name must be part of the key")
	}

	// The key must agree with CachedApprover's session cache so a caller can
	// reuse one identity for both caches.
	if want := approvalCacheKey("shell", `{"a":1,"b":2}`).argsHash; first != want {
		t.Fatalf("key %s disagrees with CachedApprover key %s", first, want)
	}
}

func TestTTLCacheKeyBoundsOversizedArgs(t *testing.T) {
	c := NewTTLCache("", time.Hour)
	one := json.RawMessage(`{"a":"` + strings.Repeat("x", maxTTLCanonicalBytes) + `"}`)
	two := json.RawMessage(`{"a":"` + strings.Repeat("x", maxTTLCanonicalBytes+1) + `"}`)
	if c.Key("shell", one) == c.Key("shell", two) {
		t.Fatal("oversized argument sets must not collide")
	}
}

func TestTTLCacheDefaultTTL(t *testing.T) {
	for _, ttl := range []time.Duration{0, -time.Second} {
		c := NewTTLCache("", ttl)
		if c.ttl != defaultTTLGrantLifetime {
			t.Fatalf("ttl %v: got %v, want %v", ttl, c.ttl, defaultTTLGrantLifetime)
		}
	}
}
