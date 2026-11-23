package tools

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// 目录列表缓存的边界。64 分片降低并发写冲突，单分片预算 1 MiB
// （总计 64 MiB），50ms 竞态窗口内的目录戳不入缓存，避免把正在变化的
// 目录列表固化下来。
const (
	dirCacheShards       = 64
	dirCacheShardBytes   = 64 << 20 / dirCacheShards // 1 MiB/分片，总计 64 MiB
	dirCacheRacyWindow   = 50 * time.Millisecond
	dirCacheMaxNameBytes = 256 // 单个文件名超过该长度不缓存，防止畸形路径撑爆预算
)

// dirEntryLite 是 os.DirEntry 的可缓存投影：只保留遍历需要的三项信息。
type dirEntryLite struct {
	name  string
	isDir bool
	kind  os.FileMode // 仅 Type 位
}

// dirStamp 是目录版本的廉价指纹。mtime + size 跨平台可得；
// Windows 上 os.SameFile 的 inode 不可直接取，故以 mtime 主导。
type dirStamp struct {
	modTime int64
	size    int64
}

// cachedDir 是一条目录列表缓存。
type cachedDir struct {
	entries []dirEntryLite
	stamp   dirStamp
	bytes   int
	gen     uint64
}

// dirCacheShard 是单个分片；LRU 用简单的切片，命中时把键移到末尾。
type dirCacheShard struct {
	mu      sync.Mutex
	entries map[string]cachedDir
	lru     []string
	bytes   int
}

// shardedDirCache 是进程级目录列表缓存。
type shardedDirCache struct {
	shards [dirCacheShards]dirCacheShard
	gen    atomic.Uint64 // ignore 源 stamp：变化时整体失效
	hits   atomic.Int64
	misses atomic.Int64
	// 只读函数变量便于测试替换（计数 readdir 次数）。
	readDir func(string) ([]os.DirEntry, error)
}

func newShardedDirCache() *shardedDirCache {
	c := &shardedDirCache{readDir: os.ReadDir}
	for i := range c.shards {
		c.shards[i].entries = make(map[string]cachedDir)
	}
	return c
}

var defaultDirCache = newShardedDirCache()

func dirCacheShardFor(key string) *dirCacheShard {
	var h uint64 = 1469598103934665603
	for i := 0; i < len(key); i++ {
		h ^= uint64(key[i])
		h *= 1099511628211
	}
	return &defaultDirCache.shards[h%dirCacheShards]
}

// dirStampOf 取目录的 mtime/size 指纹。
func dirStampOf(path string) (dirStamp, bool) {
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return dirStamp{}, false
	}
	return dirStamp{modTime: info.ModTime().UnixNano(), size: info.Size()}, true
}

// readDirCached 返回 path 的目录项列表，命中时零 readdir。
// 外部新建/删除文件会改变目录 mtime，使 stamp 失效并触发重新读取。
func readDirCached(path string) ([]dirEntryLite, error) {
	key := filepath.Clean(path)
	stamp, ok := dirStampOf(key)
	if !ok {
		// 目录不存在或无法 stat：直接读，不缓存（错误路径交给调用方处理）。
		return readDirFresh(key)
	}

	shard := dirCacheShardFor(key)
	gen := defaultDirCache.gen.Load()
	shard.mu.Lock()
	if entry, hit := shard.entries[key]; hit && entry.stamp == stamp && entry.gen == gen {
		shard.touchLocked(key)
		shard.mu.Unlock()
		defaultDirCache.hits.Add(1)
		return entry.entries, nil
	}
	shard.mu.Unlock()
	defaultDirCache.misses.Add(1)

	// 50ms 内的目录戳视为竞态：读到则先缓存 1 秒后自然过期，
	// 这里选择直接不写缓存，避免把尚未稳定的列表固化。
	if time.Since(time.Unix(0, stamp.modTime)) < dirCacheRacyWindow {
		return readDirFresh(key)
	}

	entries, err := readDirFresh(key)
	if err != nil {
		return nil, err
	}
	cached := make([]dirEntryLite, 0, len(entries))
	bytes := 0
	for _, e := range entries {
		if len(e.name) > dirCacheMaxNameBytes {
			continue // 畸形长文件名不入缓存，但仍随 entries 返回给调用方
		}
		cached = append(cached, e)
		bytes += len(e.name) + 64 // 字符串头 + 结构体近似成本
	}
	if bytes > dirCacheShardBytes || len(cached) == 0 {
		return entries, nil // 单目录超预算或全部畸形，不缓存
	}

	shard.mu.Lock()
	// 双重检查：并发读同一目录时先到者已写入。
	if cur, exists := shard.entries[key]; exists && cur.stamp == stamp && cur.gen == gen {
		shard.touchLocked(key)
		out := cur.entries
		shard.mu.Unlock()
		return out, nil
	}
	shard.evictLocked(bytes)
	shard.entries[key] = cachedDir{entries: cached, stamp: stamp, bytes: bytes, gen: gen}
	shard.lru = append(shard.lru, key)
	shard.bytes += bytes
	shard.mu.Unlock()
	return cached, nil
}

// readDirFresh 绕过缓存读一次目录。
func readDirFresh(path string) ([]dirEntryLite, error) {
	entries, err := defaultDirCache.readDir(path)
	if err != nil {
		return nil, err
	}
	return entriesToLite(entries), nil
}

func entriesToLite(entries []os.DirEntry) []dirEntryLite {
	out := make([]dirEntryLite, 0, len(entries))
	for _, e := range entries {
		out = append(out, dirEntryLite{name: e.Name(), isDir: e.IsDir(), kind: e.Type()})
	}
	return out
}

// touchLocked 把键移到 LRU 末尾（调用方持锁）。
func (s *dirCacheShard) touchLocked(key string) {
	for i, k := range s.lru {
		if k == key {
			s.lru = append(s.lru[:i], s.lru[i+1:]...)
			s.lru = append(s.lru, key)
			return
		}
	}
}

// evictLocked 按 LRU 驱逐直到能容纳 need 字节（调用方持锁）。
func (s *dirCacheShard) evictLocked(need int) {
	for len(s.lru) > 0 && s.bytes+need > dirCacheShardBytes {
		oldest := s.lru[0]
		s.lru = s.lru[1:]
		if entry, ok := s.entries[oldest]; ok {
			s.bytes -= entry.bytes
			delete(s.entries, oldest)
		}
	}
}

// invalidateDirCache 使所有分片失效（ignore 规则等发生变化时调用）。
func invalidateDirCache() {
	defaultDirCache.gen.Add(1)
}

// resetDirCache 清空所有条目并复位统计，仅供测试使用。
func resetDirCache() {
	for i := range defaultDirCache.shards {
		s := &defaultDirCache.shards[i]
		s.mu.Lock()
		s.entries = make(map[string]cachedDir)
		s.lru = nil
		s.bytes = 0
		s.mu.Unlock()
	}
	defaultDirCache.hits.Store(0)
	defaultDirCache.misses.Store(0)
}

// dirCacheCounters 返回命中/未命中次数，供测试断言二次遍历零 readdir。
func dirCacheCounters() (hits, misses int64) {
	return defaultDirCache.hits.Load(), defaultDirCache.misses.Load()
}
