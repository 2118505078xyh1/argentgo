package update

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	// StaleSuffix is appended to the previous binary while it is swapped.
	StaleSuffix = ".old"

	applyAttempts = 10
	applyDelay    = 100 * time.Millisecond
)

// Apply replaces target with the freshly downloaded dest.
//
// On Windows the running executable cannot be overwritten but can be renamed, so
// target first moves to "<name>.old", the download takes its place, and a stale
// ".old" from an earlier update is dropped first. Elsewhere a single rename is
// atomic. Renames are retried briefly because antivirus scanners and indexers
// lock fresh files for a moment.
func Apply(dest, target string) error {
	if strings.TrimSpace(dest) == "" || strings.TrimSpace(target) == "" {
		return errors.New("update: both the staged file and the target are required")
	}
	info, err := os.Stat(dest)
	if err != nil {
		return fmt.Errorf("update: staged binary %s: %w", dest, err)
	}
	if info.IsDir() {
		return fmt.Errorf("update: staged path %s is a directory", dest)
	}
	directory := filepath.Dir(target)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("update: create %s: %w", directory, err)
	}
	if runtime.GOOS == "windows" {
		return applyWindows(dest, target)
	}
	return applyUnix(dest, target)
}

func applyWindows(dest, target string) error {
	stale := target + StaleSuffix
	// The previous update may have left its .old behind (a running binary cannot
	// be deleted), so make room before renaming.
	if err := os.Remove(stale); err != nil && !errors.Is(err, os.ErrNotExist) {
		// Not fatal: the rename below surfaces the real problem.
		_ = err
	}
	hadTarget := false
	if _, err := os.Stat(target); err == nil {
		hadTarget = true
		if err := retryRename(target, stale); err != nil {
			return fmt.Errorf("update: move the current binary aside: %w", err)
		}
	}
	if err := retryRename(dest, target); err != nil {
		if hadTarget {
			_ = retryRename(stale, target)
		}
		return fmt.Errorf("update: install the new binary at %s: %w", target, err)
	}
	return nil
}

func applyUnix(dest, target string) error {
	_ = os.Remove(target + StaleSuffix)
	if err := os.Chmod(dest, 0o755); err != nil {
		return fmt.Errorf("update: mark %s executable: %w", dest, err)
	}
	if err := retryRename(dest, target); err == nil {
		return nil
	}
	// The download directory may sit on another filesystem; copy next to the
	// target and rename from there, which stays atomic.
	temp, err := os.CreateTemp(filepath.Dir(target), ".argent-install-*")
	if err != nil {
		return fmt.Errorf("update: stage beside %s: %w", target, err)
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	source, err := os.Open(dest)
	if err != nil {
		temp.Close()
		return fmt.Errorf("update: open %s: %w", dest, err)
	}
	_, copyErr := io.Copy(temp, source)
	source.Close()
	if copyErr != nil {
		temp.Close()
		return fmt.Errorf("update: copy %s: %w", dest, copyErr)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("update: flush %s: %w", tempName, err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("update: close %s: %w", tempName, err)
	}
	if err := os.Chmod(tempName, 0o755); err != nil {
		return fmt.Errorf("update: mark %s executable: %w", tempName, err)
	}
	if err := retryRename(tempName, target); err != nil {
		return fmt.Errorf("update: install the new binary at %s: %w", target, err)
	}
	_ = os.Remove(dest)
	return nil
}

func retryRename(from, to string) error {
	var err error
	for attempt := 0; attempt < applyAttempts; attempt++ {
		if err = os.Rename(from, to); err == nil {
			return nil
		}
		if attempt < applyAttempts-1 {
			time.Sleep(applyDelay)
		}
	}
	return err
}
