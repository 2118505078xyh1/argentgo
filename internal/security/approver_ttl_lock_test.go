package security

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"
)

func TestTTLCacheCorruptGrantFileFailsClosed(t *testing.T) {
	dir := t.TempDir()
	corrupt := []byte("{not valid json")
	if err := os.WriteFile(ttlGrantPath(dir), corrupt, 0o600); err != nil {
		t.Fatalf("seed corrupt grants file: %v", err)
	}
	base := time.Now()
	c := NewTTLCache(dir, time.Hour)
	c.now = func() time.Time { return base }
	key := c.Key("shell", json.RawMessage(`{"cmd":"x"}`))

	if c.IsAllowed(key) {
		t.Fatal("a corrupt grants file must not authorize any grant")
	}
	if err := c.Allow(key, "x"); err == nil {
		t.Fatal("Allow must fail closed on an untrusted grants file")
	}
	if c.IsAllowed(key) {
		t.Fatal("a rejected Allow must not create a grant")
	}
	data, err := os.ReadFile(ttlGrantPath(dir))
	if err != nil {
		t.Fatalf("read grants file: %v", err)
	}
	if !bytes.Equal(data, corrupt) {
		t.Fatalf("untrusted grants file must not be overwritten, got: %s", data)
	}

	// An explicit successful reload restores trust and the persisted grant.
	valid, err := json.Marshal([]ttlEntry{{Key: key, Readable: "x", ExpiresAt: base.Add(time.Hour)}})
	if err != nil {
		t.Fatalf("encode valid grants: %v", err)
	}
	if err := os.WriteFile(ttlGrantPath(dir), valid, 0o600); err != nil {
		t.Fatalf("write valid grants file: %v", err)
	}
	if err := c.Load(); err != nil {
		t.Fatalf("Load after repair: %v", err)
	}
	if !c.IsAllowed(key) {
		t.Fatal("grant must be honoured after a successful reload")
	}
}

func TestTTLCacheUnreadableGrantFileFailsClosed(t *testing.T) {
	dir := t.TempDir()
	// A directory at the grants path makes ReadFile fail on every platform,
	// without relying on permission bits (which Windows/admins may ignore).
	if err := os.Mkdir(ttlGrantPath(dir), 0o700); err != nil {
		t.Fatalf("create directory at grants path: %v", err)
	}
	c := NewTTLCache(dir, time.Hour)
	key := c.Key("shell", json.RawMessage(`{"cmd":"x"}`))

	if c.IsAllowed(key) {
		t.Fatal("an unreadable grants file must not authorize any grant")
	}
	if err := c.Allow(key, "x"); err == nil {
		t.Fatal("Allow must fail closed when the grants file cannot be read")
	}
}

func TestTTLCacheSharedFileWritesDoNotClobber(t *testing.T) {
	dir := t.TempDir()
	base := time.Now()
	c1 := newTTLTestCache(t, dir, time.Hour, base)
	c2 := newTTLTestCache(t, dir, time.Hour, base)
	keyA := c1.Key("shell", json.RawMessage(`{"cmd":"A"}`))
	keyB := c2.Key("shell", json.RawMessage(`{"cmd":"B"}`))

	if err := c1.Allow(keyA, "A"); err != nil {
		t.Fatalf("c1.Allow: %v", err)
	}
	// c2 has never seen keyA in memory; its write must re-read it from disk.
	if err := c2.Allow(keyB, "B"); err != nil {
		t.Fatalf("c2.Allow: %v", err)
	}
	both := newTTLTestCache(t, dir, time.Hour, base)
	if !both.IsAllowed(keyA) {
		t.Fatal("grant written by the first instance was lost")
	}
	if !both.IsAllowed(keyB) {
		t.Fatal("grant written by the second instance was lost")
	}

	// Deny through one instance must preserve the other instance's grant.
	c1.Deny(keyA)
	afterDeny := newTTLTestCache(t, dir, time.Hour, base)
	if afterDeny.IsAllowed(keyA) {
		t.Fatal("denied grant must not survive")
	}
	if !afterDeny.IsAllowed(keyB) {
		t.Fatal("deny must not clobber another instance's grant")
	}
}

func TestTTLCacheSharedFileConcurrentWrites(t *testing.T) {
	dir := t.TempDir()
	base := time.Now()
	const writers = 4
	const perWriter = 12
	caches := make([]*TTLCache, writers)
	keys := make([][]string, writers)
	for i := range caches {
		caches[i] = newTTLTestCache(t, dir, time.Hour, base)
		for j := 0; j < perWriter; j++ {
			args := json.RawMessage(fmt.Sprintf(`{"w":%d,"i":%d}`, i, j))
			keys[i] = append(keys[i], caches[i].Key("shell", args))
		}
	}

	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := range caches {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for _, key := range keys[i] {
				if err := caches[i].Allow(key, "ctx"); err != nil {
					errs <- fmt.Errorf("writer %d: %w", i, err)
					return
				}
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	final := newTTLTestCache(t, dir, time.Hour, base)
	for i := range keys {
		for _, key := range keys[i] {
			if !final.IsAllowed(key) {
				t.Fatalf("grant %s from writer %d was lost", key, i)
			}
		}
	}
}

// TestTTLCacheCrossProcessKeepsBothGrants re-executes this test binary so two
// genuinely separate processes contend for the sidecar lock. Without the
// cross-process lock, the child's write would overwrite the parent's grant.
func TestTTLCacheCrossProcessKeepsBothGrants(t *testing.T) {
	const helperEnv = "ARGENT_TTL_LOCK_HELPER"
	if os.Getenv(helperEnv) == "1" {
		c := NewTTLCache(os.Getenv("ARGENT_TTL_LOCK_DIR"), time.Hour)
		if err := c.Allow(os.Getenv("ARGENT_TTL_LOCK_KEY"), "child"); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		return
	}

	dir := t.TempDir()
	base := time.Now()
	parent := newTTLTestCache(t, dir, time.Hour, base)
	parentKey := parent.Key("shell", json.RawMessage(`{"who":"parent"}`))
	childKey := parent.Key("shell", json.RawMessage(`{"who":"child"}`))
	if err := parent.Allow(parentKey, "parent"); err != nil {
		t.Fatalf("parent Allow: %v", err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestTTLCacheCrossProcessKeepsBothGrants$")
	cmd.Env = append(os.Environ(),
		helperEnv+"=1",
		"ARGENT_TTL_LOCK_DIR="+dir,
		"ARGENT_TTL_LOCK_KEY="+childKey,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child process failed: %v\n%s", err, out)
	}

	final := newTTLTestCache(t, dir, time.Hour, base)
	if !final.IsAllowed(parentKey) {
		t.Fatal("parent grant was lost after the child process wrote")
	}
	if !final.IsAllowed(childKey) {
		t.Fatal("child grant is missing after the child process wrote")
	}
}
