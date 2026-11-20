package tools

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// repoFileStamp 是特定文件版本的廉价指纹。mtime
// （UnixNano）加上大小，正是 aider 的 repomap 用来判断
// 用于判断缓存标签是否仍然有效；命中时它从不读取文件内容。
type repoFileStamp struct {
	modTime int64
	size    int64
}

// repoCacheKey 索引一份缓存的解析结果。root 防止两个相对布局
// 相同的工作区共享条目；rel + stamp 标识
// 精确的文件版本。
type repoCacheKey struct {
	root  string
	rel   string
	stamp repoFileStamp
}

// symbolCache 是进程内已解析 Go 符号的缓存。它
// 刻意只在内存中：不向磁盘写入任何内容。未命中时读+解析期间持有互斥锁，
// 使单个文件版本恰好被解析
// 一次，即使 repo_map 被并发调用（例如被子代理调用）。
var symbolCache = struct {
	mu     sync.Mutex
	files  map[repoCacheKey][]repoSymbol
	hits   int
	misses int
}{files: make(map[repoCacheKey][]repoSymbol)}

// statRepoFileStamp 返回普通文件的 mtime/size 指纹。
func statRepoFileStamp(path string) (repoFileStamp, bool) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return repoFileStamp{}, false
	}
	return repoFileStamp{modTime: info.ModTime().UnixNano(), size: info.Size()}, true
}

// cachedRepoSymbols 返回某个 Go 文件解析后的顶层符号，
// 在文件 mtime 和 size 未变化时复用先前的解析结果。
// 返回的切片是副本，调用方无法修改缓存值。布尔
// 结果报告是否命中了缓存。
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

// cachedRepoSymbolsForFiles 是带缓存感知的对应实现，
// 对应 extractRepoSymbols：它消费同一份（已发现的）相对文件
// 列表，返回逐字节一致的符号，在文件戳未变化时复用缓存解析。
// 它是加速的唯一接线点；
// repo_map.go 的 Run 路径无需改动。
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

// clearRepoSymbolCache 丢弃所有缓存条目并重置统计信息。
// 在测试中使用，或在应强制重新解析的批量文件系统改动之后使用。
// 解析。
func clearRepoSymbolCache() {
	symbolCache.mu.Lock()
	symbolCache.files = make(map[repoCacheKey][]repoSymbol)
	symbolCache.hits = 0
	symbolCache.misses = 0
	symbolCache.mu.Unlock()
}

// invalidateRepoSymbolCacheRoot 只丢弃属于某个 root 的条目。
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

// fitBudgetTolerancePercent 是所选前缀允许超出标称预算的程度。
// aider 的 repomap 容忍少量超出，以免一个稍长的
// 行被无谓丢弃；我们同样使用 15%。
const fitBudgetTolerancePercent = 15

// fitBudget 返回行的最长前缀，其合计估算
// token 成本保持在预算内，允许 15% 的超出。成本来自
// estimateRepoTokens，它把宽 CJK rune 大致按一个 token 计
// 而非三个字节：较长的中文行不再被低估。
// 只按整行处理，因此多字节 rune 绝不会被切分。前缀和加上
// sort.Search 给出对切分点的二分查找（O(n) 求前缀和，O(log n)
// 查找）。
func fitBudget(lines []string, budget int) []string {
	if budget <= 0 || len(lines) == 0 {
		return nil
	}
	limit := budget + budget*fitBudgetTolerancePercent/100

	prefix := make([]int, len(lines)+1)
	for i, line := range lines {
		prefix[i+1] = prefix[i] + estimateRepoTokens(line)
	}
	// 累计成本超过上限的第一个下标；答案是它
	// 前面的那个。prefix[0] == 0 <= limit，因此结果至少为 1，
	// 且 n 绝不会为负。
	n := sort.Search(len(prefix), func(i int) bool { return prefix[i] > limit }) - 1
	if n <= 0 {
		return nil
	}
	return lines[:n]
}
