package agent

import (
	"fmt"
	"strings"
	"sync"
)

// 本文件实现 TODO #33 的"会话内易失工具结果缓存"。它是 agent 包内的
// 独立原语：按模型轮次推进（advanceIteration），对连续 5 轮未被 read
// 命中的条目做空闲淘汰，总字节预算 16 MiB，超出预算时按 LRU 淘汰。
//
// 与 tools 包的关系：tools 侧有自己的默认缓存，让 clip()/read 的默认
// 路径无需装配即可产出 argent://cache URI；本文件提供循环侧使用的
// 完整实现，待 loop 接线后由本包的 storeToolResult/loadToolResult/
// advanceToolResultIteration 接管同一语义。两者都不重放任何工具副作用。

const (
	// toolResultCacheCapBytes 是缓存的总字节预算（16 MiB）。
	toolResultCacheCapBytes = 16 << 20
	// toolResultCacheIdleRounds 是连续未被 read 命中的淘汰阈值（轮）。
	toolResultCacheIdleRounds = 5
	toolResultCacheURIPrefix  = "argent://cache/"
	toolResultCacheURISuffix  = ".result.txt"
	// toolResultCacheMissText 明确要求模型不要重放有副作用的操作。
	toolResultCacheMissText = "argent://cache 结果已过期（会话内结果缓存空闲 5 轮后淘汰）；" +
		"请勿重放有副作用的操作来取回输出，改用只读工具重新获取。" +
		" DO NOT repeat side-effecting actions to recover output."
)

// toolResultCacheEntry 是缓存中的一条记录；usedAt 是最后一次被 Load
// 命中时的轮次，Store 时记为当前轮。
type toolResultCacheEntry struct {
	text   string
	bytes  int
	usedAt int
}

// toolResultCache 是纯标准库的 LRU + 字节预算缓存，所有方法持锁，
// 因为 dispatch 与 read 解析可能并发。
type toolResultCache struct {
	mu        sync.Mutex
	entries   map[string]toolResultCacheEntry
	lru       []string
	iteration int
	nextID    uint64
	capBytes  int
}

func newToolResultCache(capBytes int) *toolResultCache {
	if capBytes <= 0 {
		capBytes = toolResultCacheCapBytes
	}
	return &toolResultCache{
		entries:  make(map[string]toolResultCacheEntry),
		capBytes: capBytes,
	}
}

// store 缓存 text 并返回 argent://cache/<id>.result.txt。单条超过
// 总预算时不缓存（返回空串），由调用方回落到普通裁剪。
func (c *toolResultCache) store(text string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(text) > c.capBytes {
		return ""
	}
	c.nextID++
	key := fmt.Sprintf("%016x", c.nextID)
	c.entries[key] = toolResultCacheEntry{text: text, bytes: len(text), usedAt: c.iteration}
	c.lru = append(c.lru, key)
	for c.bytesLocked() > c.capBytes && len(c.lru) > 0 {
		c.evictOldestLocked()
	}
	return toolResultCacheURI(key)
}

// load 解析 URI。handled=false 表示不是本缓存的 URI；handled=true 时
// result 为缓存正文，或已过期时的可读提示（绝不重放工具副作用）。
func (c *toolResultCache) load(uri string) (result string, handled bool) {
	key, ok := parseToolResultCacheURI(uri)
	if !ok {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, found := c.entries[key]
	if !found {
		return c.missHint(), true
	}
	entry.usedAt = c.iteration
	c.entries[key] = entry
	c.touchLocked(key)
	return entry.text, true
}

// advanceIteration 推进一轮，并淘汰连续 toolResultCacheIdleRounds 轮
// 未被 read 命中的条目。每次模型轮次结束调用一次。
func (c *toolResultCache) advanceIteration() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.iteration++
	keep := c.lru[:0]
	for _, key := range c.lru {
		if c.iteration-c.entries[key].usedAt >= toolResultCacheIdleRounds {
			delete(c.entries, key)
			continue
		}
		keep = append(keep, key)
	}
	c.lru = keep
}

// missHint 返回缓存未命中的可读提示；提示本身不触发任何工具调用。
func (c *toolResultCache) missHint() string {
	return toolResultCacheMissText
}

// bytesLocked 返回当前缓存的总字节数；调用方必须持锁。
func (c *toolResultCache) bytesLocked() int {
	total := 0
	for _, e := range c.entries {
		total += e.bytes
	}
	return total
}

// evictOldestLocked 淘汰 LRU 队首；调用方必须持锁。
func (c *toolResultCache) evictOldestLocked() {
	if len(c.lru) == 0 {
		return
	}
	oldest := c.lru[0]
	c.lru = c.lru[1:]
	delete(c.entries, oldest)
}

// touchLocked 把 key 移到 LRU 队尾；调用方必须持锁。
func (c *toolResultCache) touchLocked(key string) {
	for i, k := range c.lru {
		if k == key {
			copy(c.lru[i:], c.lru[i+1:])
			c.lru[len(c.lru)-1] = key
			return
		}
	}
}

// toolResultCacheURI 由 id 生成稳定的缓存 URI。
func toolResultCacheURI(id string) string {
	return toolResultCacheURIPrefix + id + toolResultCacheURISuffix
}

// parseToolResultCacheURI 从 URI 中提取 id；格式不符时返回 false。
func parseToolResultCacheURI(uri string) (string, bool) {
	if !strings.HasPrefix(uri, toolResultCacheURIPrefix) ||
		!strings.HasSuffix(uri, toolResultCacheURISuffix) {
		return "", false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(uri, toolResultCacheURIPrefix), toolResultCacheURISuffix)
	if id == "" || strings.ContainsAny(id, `/\\`) {
		return "", false
	}
	return id, true
}

// 进程级默认缓存。loop 接线后由同一包内的 loop/dispatch 调用下面三个
// 薄封装，避免把实现细节暴露到 agent 包之外。
var defaultToolResultCache = newToolResultCache(toolResultCacheCapBytes)

func storeToolResult(text string) string {
	return defaultToolResultCache.store(text)
}

func loadToolResult(uri string) (string, bool) {
	return defaultToolResultCache.load(uri)
}

func advanceToolResultIteration() {
	defaultToolResultCache.advanceIteration()
}
