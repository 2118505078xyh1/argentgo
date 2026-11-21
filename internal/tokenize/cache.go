package tokenize

import (
	"container/list"
	"crypto/sha256"
	"sync"
)

// cacheCapacity 是估算缓存可容纳的内容条目上限。
// 取有限容量，避免长时间运行的会话中缓存随不同内容无界增长。
const cacheCapacity = 512

// cacheKey 是内容的 SHA-256 摘要，作为缓存的稳定键：
// 相同内容得到相同键，不同内容得到不同键。
type cacheKey [sha256.Size]byte

// cacheEntry 同时保存原始文本与估算结果。
// 命中时比对原始文本，确保即便哈希碰撞也绝不返回错误结果。
type cacheEntry struct {
	key   cacheKey
	text  string
	value int
}

// estimateCache 是一个有上限的、并发安全的 LRU 缓存。
// order 的队首为最近使用，队尾为最久未使用；items 用哈希键索引链表节点。
type estimateCache struct {
	mu       sync.Mutex
	capacity int
	items    map[cacheKey]*list.Element
	order    *list.List
	hitCount int // 命中次数，供测试与诊断使用
}

// sharedCache 是包级共享的估算缓存。
var sharedCache = newEstimateCache(cacheCapacity)

func newEstimateCache(capacity int) *estimateCache {
	return &estimateCache{
		capacity: capacity,
		items:    make(map[cacheKey]*list.Element, capacity),
		order:    list.New(),
	}
}

// hashContent 返回 text 的内容哈希，作为稳定的缓存键。
func hashContent(text string) cacheKey {
	return sha256.Sum256([]byte(text))
}

// get 命中时返回缓存值与 true。
// 即使哈希相同也会比对完整文本，确保不同内容绝不误命中。
func (c *estimateCache) get(key cacheKey, text string) (int, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	elem, ok := c.items[key]
	if !ok {
		return 0, false
	}
	entry := elem.Value.(*cacheEntry)
	if entry.text != text {
		// 哈希碰撞：按未命中处理，保留原有条目。
		return 0, false
	}
	c.order.MoveToFront(elem)
	c.hitCount++
	return entry.value, true
}

// put 把一次估算结果写入缓存，必要时淘汰最久未使用的条目。
func (c *estimateCache) put(key cacheKey, text string, value int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.capacity <= 0 {
		return
	}
	if elem, ok := c.items[key]; ok {
		entry := elem.Value.(*cacheEntry)
		if entry.text == text {
			entry.value = value
			c.order.MoveToFront(elem)
			return
		}
		// 哈希碰撞：移除旧条目后再插入新条目。
		c.order.Remove(elem)
		delete(c.items, key)
	}
	if c.order.Len() >= c.capacity {
		if oldest := c.order.Back(); oldest != nil {
			oldEntry := oldest.Value.(*cacheEntry)
			c.order.Remove(oldest)
			delete(c.items, oldEntry.key)
		}
	}
	entry := &cacheEntry{key: key, text: text, value: value}
	c.items[key] = c.order.PushFront(entry)
}

// stats 返回缓存的命中次数与当前条目数，供测试使用。
func (c *estimateCache) stats() (hits, size int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hitCount, c.order.Len()
}
