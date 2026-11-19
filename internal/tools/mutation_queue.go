package tools

import (
	"path/filepath"
	"strings"
	"sync"
)

// fileMutationLocks maps a canonical file key to the mutex that serializes the
// whole read-modify-write of that file. Distinct keys keep independent mutexes,
// so writes to unrelated files still run in parallel.
var fileMutationLocks sync.Map // map[string]*sync.Mutex

// fileMutationKey builds the queue key for path. It mirrors omp's realpath
// rule: resolve symlinks for the nearest existing ancestor, then append the
// missing relative suffix. A path that does not exist yet therefore shares a
// key with any other spelling (or symlink route) that lands on the same file.
// Windows paths are case-folded; EvalSymlinks failures fall back to Abs+Clean.
func fileMutationKey(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = filepath.Clean(path)
	} else {
		abs = filepath.Clean(abs)
	}

	candidate := abs
	var suffix []string
	for {
		if resolved, err := filepath.EvalSymlinks(candidate); err == nil {
			key := filepath.Clean(resolved)
			// suffix was collected from the deepest segment upwards.
			for i := len(suffix) - 1; i >= 0; i-- {
				key = filepath.Join(key, suffix[i])
			}
			return strings.ToLower(filepath.Clean(key))
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			// Reached the volume root without a resolvable ancestor.
			return strings.ToLower(abs)
		}
		suffix = append(suffix, filepath.Base(candidate))
		candidate = parent
	}
}

func fileMutationMutex(path string) *sync.Mutex {
	key := fileMutationKey(path)
	mu, _ := fileMutationLocks.LoadOrStore(key, &sync.Mutex{})
	return mu.(*sync.Mutex)
}

// lockFileMutation acquires the per-file lock and returns its unlock function.
// Callers must defer the unlock so an early error cannot strand the lock.
func lockFileMutation(path string) func() {
	mu := fileMutationMutex(path)
	mu.Lock()
	return mu.Unlock
}

// withFileMutation runs fn under the file's mutation lock. The lock covers the
// entire read-modify-write, not just the final write syscall.
func withFileMutation(path string, fn func()) {
	unlock := lockFileMutation(path)
	defer unlock()
	fn()
}
