package automation

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// errBusy reports that an id already has an in-flight run. It is the single
// claim signal shared by the daemon loop and RunNow, so both paths honour the
// same "at most one run per id" rule even in different processes.
var errBusy = errors.New("automation: task is already running")

// staleClaimAfter is how old a claim must be before a fresh process treats it
// as abandoned. A normal run releases its claim on completion; this only
// recovers from a process that died mid-run. It is deliberately long so a
// slow-but-live task is never duplicated.
const staleClaimAfter = time.Hour

// claimDir holds the cross-process in-flight markers and the per-id run
// counters. It lives in the store directory, which every caller of RunNow and
// every daemon shares, even when their log directories differ.
func (s *Store) claimDir() string { return filepath.Join(s.dir, ".claims") }

// claimFileName maps an arbitrary (possibly hand-written) task id to a safe,
// stable file name without trusting it as a path component.
func claimFileName(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])
}

// claim atomically reserves id for one run and returns the run's monotonic
// sequence number. The reservation is a file created with O_EXCL, so it is
// visible to the daemon and to RunNow even when they run in separate processes.
// A caller that gets a nil error must call release when its run finishes.
func (s *Store) claim(id string) (uint64, error) {
	dir := s.claimDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	key := claimFileName(id)
	lock := filepath.Join(dir, key+".lock")
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if errors.Is(err, fs.ErrExist) && claimIsStale(lock) {
		// A crashed process cannot release its claim; reclaim it after the
		// grace period so a restarted daemon is not wedged forever.
		_ = os.Remove(lock)
		f, err = os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	}
	if err != nil {
		return 0, errBusy
	}
	_, _ = fmt.Fprintf(f, "%d\n", time.Now().UnixNano())
	_ = f.Close()
	seq, err := s.nextSeq(dir, key)
	if err != nil {
		_ = os.Remove(lock)
		return 0, err
	}
	return seq, nil
}

// release drops the in-flight reservation for id. Releasing an id that was
// never claimed is harmless.
func (s *Store) release(id string) {
	_ = os.Remove(filepath.Join(s.claimDir(), claimFileName(id)+".lock"))
}

// nextSeq returns the next per-id run sequence. It is only ever called while
// the matching claim is held, so the read-modify-write is safe across
// processes. The counter survives release and restarts, which makes it a
// stable, collision-free basis for log file names.
func (s *Store) nextSeq(dir, key string) (uint64, error) {
	path := filepath.Join(dir, key+".seq")
	var n uint64
	if data, err := os.ReadFile(path); err == nil {
		n, _ = strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return 0, err
	}
	n++
	if err := writeAtomic(path, []byte(strconv.FormatUint(n, 10)+"\n")); err != nil {
		return 0, err
	}
	return n, nil
}

// claimIsStale reports whether a claim file is old enough to be abandoned.
// An unreadable or malformed marker is treated as stale so it cannot wedge the
// task permanently.
func claimIsStale(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var stamp int64
	if _, err := fmt.Sscan(string(data), &stamp); err != nil {
		return true
	}
	return time.Since(time.Unix(0, stamp)) > staleClaimAfter
}
