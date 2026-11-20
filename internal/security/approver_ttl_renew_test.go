package security

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestTTLCacheExpiredCleanupKeepsConcurrentlyRenewedGrant covers a race between
// an observer whose in-memory copy of a grant has expired and another instance
// that renews the same grant (under the cross-process lock) while the observer
// is on its way to that lock. The cleanup must re-check the latest on-disk
// expiry before deleting: an unconditional delete erases the fresh grant that
// was just written, so neither this process nor a fresh one honours it.
func TestTTLCacheExpiredCleanupKeepsConcurrentlyRenewedGrant(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().Truncate(time.Second)
	const ttl = time.Hour
	expiredAt := base.Add(ttl + time.Second)

	writer := newTTLTestCache(t, dir, ttl, base)
	key := writer.Key("shell", json.RawMessage(`{"cmd":"x"}`))
	if err := writer.Allow(key, "original"); err != nil {
		t.Fatalf("Allow: %v", err)
	}

	// The observer loaded the original grant while it was still live.
	observer := NewTTLCache(dir, ttl)
	observer.now = func() time.Time { return base }
	if !observer.IsAllowed(key) {
		t.Fatal("observer should see the original grant before its expiry")
	}

	// Pin the observer past the original expiry. Its first clock read is the
	// one in IsAllowed, before it reaches the cross-process lock; hold it there
	// until the renewer has written a fresh grant for the same key.
	started := make(chan struct{})
	renewed := make(chan struct{})
	var once sync.Once
	observer.now = func() time.Time {
		once.Do(func() {
			close(started)
			<-renewed
		})
		return expiredAt
	}

	allowed := make(chan bool, 1)
	go func() { allowed <- observer.IsAllowed(key) }()
	<-started

	renewer := newTTLTestCache(t, dir, ttl, expiredAt)
	if err := renewer.Allow(key, "renewed"); err != nil {
		t.Fatalf("renewer Allow: %v", err)
	}
	close(renewed)

	if got := <-allowed; !got {
		t.Fatal("IsAllowed must honour a grant that another instance renewed concurrently")
	}

	// A fresh process must still see the renewed grant on disk: the expired
	// cleanup must not have overwritten it.
	final := newTTLTestCache(t, dir, ttl, expiredAt)
	if !final.IsAllowed(key) {
		t.Fatal("the renewed grant was erased from disk by the expired cleanup")
	}
	data, err := os.ReadFile(ttlGrantPath(dir))
	if err != nil {
		t.Fatalf("read grants file: %v", err)
	}
	if !strings.Contains(string(data), key) {
		t.Fatalf("renewed key missing from grants file: %s", data)
	}
}
