package automation

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestClaimIsStaleFreshUnstampedMarker captures the window between O_EXCL
// creating a lock file and the owner writing its stamp. The marker is empty
// but brand new; treating it as an abandoned claim lets a second caller steal
// a run that is already in flight.
func TestClaimIsStaleFreshUnstampedMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fresh.lock")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if claimIsStale(path) {
		t.Fatal("a just-created, still-empty claim must not be considered stale")
	}
}

// TestClaimIsStaleUnusableMarker verifies the documented contract: a marker
// that cannot be read as a regular file must be reclaimable rather than
// wedging the task forever. A directory at the lock path is the portable way
// to make the marker unusable on every platform.
func TestClaimIsStaleUnusableMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blocked.lock")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if !claimIsStale(path) {
		t.Fatal("an unusable claim marker must be considered stale")
	}
}

func TestClaimIsStaleByModificationTime(t *testing.T) {
	dir := t.TempDir()
	fresh := filepath.Join(dir, "fresh.lock")
	if err := os.WriteFile(fresh, []byte(fmt.Sprintf("%d\n", time.Now().UnixNano())), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if claimIsStale(fresh) {
		t.Fatal("a fresh claim with a valid stamp must not be stale")
	}

	old := filepath.Join(dir, "old.lock")
	if err := os.WriteFile(old, []byte(fmt.Sprintf("%d\n", time.Now().UnixNano())), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	stamp := time.Now().Add(-2 * staleClaimAfter)
	if err := os.Chtimes(old, stamp, stamp); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
	if !claimIsStale(old) {
		t.Fatal("an abandoned claim past the grace period must be stale")
	}
}

// TestClaimRejectsFreshUnstampedLock is the end-to-end version of the race:
// while one caller owns a just-created lock (before its stamp is written), a
// second caller must get errBusy instead of stealing the claim and running the
// same task twice.
func TestClaimRejectsFreshUnstampedLock(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	lock := filepath.Join(store.claimDir(), claimFileName("nightly")+".lock")
	if err := os.MkdirAll(filepath.Dir(lock), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// Simulate the instant right after the first caller's O_EXCL succeeded.
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, err := store.claim("nightly"); !errors.Is(err, errBusy) {
		t.Fatalf("claim over a fresh unstamped lock = %v, want errBusy", err)
	}
	if _, err := os.Stat(lock); err != nil {
		t.Fatalf("the original claim marker was stolen: %v", err)
	}
}

// TestClaimReclaimsAbandonedLock is the positive counterpart: a genuinely old
// claim must still be recoverable so a crashed process cannot wedge a task.
func TestClaimReclaimsAbandonedLock(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	lock := filepath.Join(store.claimDir(), claimFileName("nightly")+".lock")
	if err := os.MkdirAll(filepath.Dir(lock), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(lock, []byte("stale\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	stamp := time.Now().Add(-2 * staleClaimAfter)
	if err := os.Chtimes(lock, stamp, stamp); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	seq, err := store.claim("nightly")
	if err != nil {
		t.Fatalf("claim over an abandoned lock: %v", err)
	}
	if seq == 0 {
		t.Fatal("claim returned a zero sequence number")
	}
	defer store.release("nightly")
	if info, err := os.Stat(lock); err != nil || info.Mode()&os.ModeType != 0 {
		t.Fatalf("reclaimed claim is not a regular file: info=%v err=%v", info, err)
	}
}
