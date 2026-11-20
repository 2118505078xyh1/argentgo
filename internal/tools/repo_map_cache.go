package tools

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// repoFileStamp is a cheap fingerprint of a specific file version. mtime
// (UnixNano) plus size is exactly the signal aider's repomap uses to decide
// whether cached tags are still valid; it never reads file contents on a hit.
type repoFileStamp struct {
	modTime int64
	size    int64
}

// repoCacheKey indexes one cached parse result. root keeps two workspaces with
// identical relative layouts from sharing entries; rel + stamp identify the
// exact file version.
type repoCacheKey struct {
	root  string
	rel   string
	stamp repoFileStamp
}

// symbolCache is a process-local cache of parsed Go symbols. It is
// deliberately in-memory only: nothing is written to disk. The mutex is held
// across the read+parse on a miss so a single file version is parsed exactly
// once even when repo_map is invoked concurrently (e.g. by subagents).
var symbolCache = struct {
	mu     sync.Mutex
	files  map[repoCacheKey][]repoSymbol
	hits   int
	misses int
}{files: make(map[repoCacheKey][]repoSymbol)}

// statRepoFileStamp returns the mtime/size fingerprint of a regular file.
func statRepoFileStamp(path string) (repoFileStamp, bool) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return repoFileStamp{}, false
	}
	return repoFileStamp{modTime: info.ModTime().UnixNano(), size: info.Size()}, true
}

// cachedRepoSymbols returns the parsed top-level symbols for one Go file,
// reusing a previous parse when the file's mtime and size are unchanged. The
// returned slice is a copy so callers cannot mutate the cached value. The bool
// result reports whether the cache was hit.
func cachedRepoSymbols(base, rel string) ([]repoSymbol, bool) {
	if !strings.HasSuffix(strings.ToLower(rel), ".go") {
		return nil, false
	}
	path := filepath.Join(base, filepath.FromSlash(rel))
	stamp, ok := statRepoFileStamp(path)
	if !ok {
		return nil, false
	}
	key := repoCacheKey{
		root:  filepath.Clean(base),
		rel:   filepath.ToSlash(rel),
		stamp: stamp,
	}

	symbolCache.mu.Lock()
	defer symbolCache.mu.Unlock()
	if syms, hit := symbolCache.files[key]; hit {
		symbolCache.hits++
		return append([]repoSymbol(nil), syms...), true
	}
	symbolCache.misses++
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	syms := parseGoSource(rel, src)
	symbolCache.files[key] = syms
	return append([]repoSymbol(nil), syms...), false
}

// cachedRepoSymbolsForFiles is the cache-aware counterpart of
// extractRepoSymbols: it consumes the same (already discovered) relative file
// list and returns byte-for-byte identical symbols, reusing cached parses when
// file stamps are unchanged. It is the single wiring point for acceleration;
// repo_map.go's Run path needs no change.
func cachedRepoSymbolsForFiles(base string, relFiles []string) []repoSymbol {
	files := append([]string(nil), relFiles...)
	sort.Strings(files)
	var out []repoSymbol
	for _, rel := range files {
		if !strings.HasSuffix(strings.ToLower(rel), ".go") {
			continue
		}
		syms, _ := cachedRepoSymbols(base, rel)
		out = append(out, syms...)
	}
	return out
}

// clearRepoSymbolCache drops every cached entry and resets the statistics.
// Use it in tests or after bulk filesystem changes that should force a fresh
// parse.
func clearRepoSymbolCache() {
	symbolCache.mu.Lock()
	symbolCache.files = make(map[repoCacheKey][]repoSymbol)
	symbolCache.hits = 0
	symbolCache.misses = 0
	symbolCache.mu.Unlock()
}

// invalidateRepoSymbolCacheRoot drops only the entries belonging to one root.
func invalidateRepoSymbolCacheRoot(base string) {
	clean := filepath.Clean(base)
	symbolCache.mu.Lock()
	for k := range symbolCache.files {
		if k.root == clean {
			delete(symbolCache.files, k)
		}
	}
	symbolCache.mu.Unlock()
}

type repoCacheStats struct {
	entries int
	hits    int
	misses  int
}

func repoSymbolCacheStats() repoCacheStats {
	symbolCache.mu.Lock()
	defer symbolCache.mu.Unlock()
	return repoCacheStats{
		entries: len(symbolCache.files),
		hits:    symbolCache.hits,
		misses:  symbolCache.misses,
	}
}

// fitBudgetTolerancePercent is how far past the nominal budget a selected
// prefix may go. aider's repomap tolerates a small overshoot so a slightly
// too-long line is not dropped needlessly; we use the same 15%.
const fitBudgetTolerancePercent = 15

// fitBudget returns the longest prefix of lines whose combined estimated
// token cost stays within budget, allowing a 15% overshoot. Costs use the same
// len(bytes)/4 estimate as the rest of the tool, so CJK runs are counted by
// bytes but never split: whole lines only. Prefix sums plus sort.Search give a
// binary search over the cut point (O(n) sums, O(log n) search).
func fitBudget(lines []string, budget int) []string {
	if budget <= 0 || len(lines) == 0 {
		return nil
	}
	limit := budget + budget*fitBudgetTolerancePercent/100

	prefix := make([]int, len(lines)+1)
	for i, line := range lines {
		prefix[i+1] = prefix[i] + repoMapTokenEstimate(line)
	}
	// First index whose cumulative cost exceeds the limit; the answer is the
	// one before it. prefix[0] == 0 <= limit, so the result is at least 1 and
	// n is never negative.
	n := sort.Search(len(prefix), func(i int) bool { return prefix[i] > limit }) - 1
	if n <= 0 {
		return nil
	}
	return lines[:n]
}
