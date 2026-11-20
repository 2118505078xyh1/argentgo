package tools

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultSpillRetention is the age past which a spill artifact becomes eligible
// for cleanup. Seven days keeps recovered output around for a typical debugging
// session while bounding the disk footprint of a long-lived runtime.
const DefaultSpillRetention = 7 * 24 * time.Hour

// CleanSpillArtifacts removes canonical spill artifacts in dir whose mtime is
// older than maxAge. It is deliberately conservative so it can be called
// unconditionally at session start without risking user data:
//
//   - only files matching the exact artifact-name contract are considered, so
//     unrelated files that happen to live in the directory are never touched;
//   - a missing directory is a no-op and an unreadable one is not an error,
//     which makes repeated calls idempotent and safe on fresh installations;
//   - an individual removal failure is skipped, never aborting the sweep;
//   - maxAge <= 0 disables cleanup entirely rather than deleting everything,
//     so a misconfigured caller cannot wipe the directory.
//
// The returned count is for observability/tests only. Production callers must
// ignore the error: retention is best-effort and must never fail a tool run.
func CleanSpillArtifacts(dir string, maxAge time.Duration) (removed int, err error) {
	if maxAge <= 0 || strings.TrimSpace(dir) == "" {
		return 0, nil
	}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return 0, nil
		}
		return 0, readErr
	}
	cutoff := time.Now().Add(-maxAge)
	for _, entry := range entries {
		if !spillArtifactName(entry.Name()) {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil || !info.Mode().IsRegular() || !info.ModTime().Before(cutoff) {
			continue
		}
		if os.Remove(filepath.Join(dir, entry.Name())) == nil {
			removed++
		}
	}
	return removed, nil
}
