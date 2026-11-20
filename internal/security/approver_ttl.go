package security

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// 当调用方传入非正数 ttl 时使用 defaultTTLGrantLifetime。
	defaultTTLGrantLifetime = 30 * time.Minute
	// maxTTLGrantEntries 限制内存中和磁盘上的授予集合大小。
	maxTTLGrantEntries = 1000
	// maxTTLCanonicalBytes 限制 Key 检查的参数文本量。
	maxTTLCanonicalBytes = 64 * 1024
	// maxTTLReadableRunes 限制每条授予保留的人类可读上下文。
	maxTTLReadableRunes = 200
	// ttlGrantsFile 是在 dir 内创建的持久化文件。
	ttlGrantsFile = "grants.json"
	// ttlGrantsLockFile 是用于跨进程锁的 sidecar 文件。操作系统拥有
	// 该锁并在进程退出时释放它，因此它永远不会陈旧。
	ttlGrantsLockFile = "grants.json.lock"
)

// 当持久化的授予集合无法被读取或解码时，Allow 返回 errTTLUntrusted。
// 缓存拒绝授予或覆盖它无法信任的文件；必须显式成功调用 Load
// 才能恢复。
var errTTLUntrusted = errors.New("security: ttl grants untrusted")

// ttlEntry 是一条持久化的授予。ExpiresAt 是绝对墙上时钟时间，
// 因此由前一个进程写入的授予在重启后保持相同的剩余生存期。
// Readable 在写入前会被脱敏并按 rune 限制长度，
// 因此文件永远不会保存凭证。
type ttlEntry struct {
	Key       string    `json:"key"`
	Readable  string    `json:"readable"`
	ExpiresAt time.Time `json:"expires_at"`
}

// TTLCache 是一个自包含的审批授予缓存，带过期时间和可选的 JSON 持久化。
// 与 CachedApprover 不同，它会使授予过期，并在提供目录时
// 能跨重启存活。拒绝永不被记录。
//
// 授予以 JSON 数组形式存储在 <dir>/grants.json 中，以原子方式写入
// （临时文件 + sync + rename，权限 0600）。过期授予在读取和写入时
// 都会被丢弃，集合大小上限为 maxTTLGrantEntries。空目录使缓存
// 成为纯内存的。
//
// 持久化时，每次写入都会在 sidecar 文件上获取跨进程锁
// <dir>/grants.json.lock，
// 从磁盘重新读取最新的授予集合，然后才应用更改，因此
// 两个进程不会互相覆盖对方的授予。无法读取或解码的文件会
// 失败即拒绝：IsAllowed 返回 false，Allow 返回错误，
// 而不是静默地从空集合（或全新集合）开始。
type TTLCache struct {
	mu      sync.Mutex
	dir     string
	ttl     time.Duration
	entries map[string]ttlEntry
	// now 是过期时钟。测试用固定时钟替换它；它在构造时设置一次，
	// 此后永不再改变。
	now func() time.Time
	// 当持久化授予文件无法被信任时设置 failed。设置期间，
	// 不兑现任何授予，也不尝试任何写入。成功的 Load
	// （或重新读取磁盘成功的写入）会清除它。
	failed bool
}

// NewTTLCache 创建一个 ttl 的缓存（非正数时默认 30 分钟）。
// 持久化的授予从磁盘加载；文件缺失没问题，但读取或
// 解码失败会把缓存标记为不可信，使其失败即拒绝，而不是
// 把损坏的文件当作空授予集合。
func NewTTLCache(dir string, ttl time.Duration) *TTLCache {
	if ttl <= 0 {
		ttl = defaultTTLGrantLifetime
	}
	c := &TTLCache{
		dir:     dir,
		ttl:     ttl,
		entries: make(map[string]ttlEntry),
		now:     time.Now,
	}
	if dir != "" {
		if err := c.Load(); err != nil {
			c.failed = true
		}
	}
	return c
}

// Key 返回一个 (tool, args) 授予的稳定十六进制 SHA-256 标识。它
// 镜像 approvalCacheKey，使 CachedApprover 的调用方能复用同一个键：
// sha256(tool + "\x00" + canonicalArgs(args))。
func (c *TTLCache) Key(tool string, args json.RawMessage) string {
	sum := sha256.Sum256([]byte(tool + "\x00" + canonicalTTLArgs(args)))
	return hex.EncodeToString(sum[:])
}

// canonicalTTLArgs 镜像 approver.go 的 canonicalArgs：对象键由
// json.Marshal 递归排序，UseNumber 保持 1 和 1.0 这样的数字写法
// 彼此不同，尾随数据回退为空白压缩后的文本。
// 输入上限为 64 KiB；超大参数会得到一个带长度标记的前缀，
// 使两组长参数不会碰撞到同一个键上。
func canonicalTTLArgs(raw json.RawMessage) string {
	if len(raw) > maxTTLCanonicalBytes {
		return fmt.Sprintf("oversized:%d:%s", len(raw), string(raw[:maxTTLCanonicalBytes]))
	}
	s := strings.TrimSpace(string(raw))
	if s == "" {
		return "{}"
	}
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return strings.Join(strings.Fields(s), " ")
	}
	// 拒绝尾随数据，使带后缀的值不会共享裸值的键，
	// 与 canonicalArgs 中的 json.Unmarshal 一致。
	if err := dec.Decode(new(any)); err != io.EOF {
		return strings.Join(strings.Fields(s), " ")
	}
	normalized, err := json.Marshal(value)
	if err != nil {
		return s
	}
	return string(normalized)
}

// Allow 为 key 记录一条授予，其生存期到 now+ttl。readable 在
// 存储或写入磁盘前会被清理、脱敏和限制长度。对于持久化的缓存，
// 在添加授予之前会在跨进程锁下重新读取磁盘上的集合，
// 从而保留并发写入者的授予。持久化写入失败会被返回，但内存中的
// 持久化写入失败会被返回，但内存中的授予仍然
// 对当前进程生效；不可信的授予文件则会失败即拒绝。
func (c *TTLCache) Allow(key, readable string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failed {
		return errTTLUntrusted
	}
	now := c.now()
	if c.dir == "" {
		purgeGrantEntries(c.entries, now)
		c.entries[key] = newTTLGrant(key, readable, now.Add(c.ttl))
		capGrantEntries(c.entries)
		return nil
	}
	return c.withGrantLock(func() error {
		entries, _, err := c.readGrantsLocked()
		if err != nil {
			c.failed = true
			return err
		}
		purgeGrantEntries(entries, now)
		entries[key] = newTTLGrant(key, readable, now.Add(c.ttl))
		capGrantEntries(entries)
		// 写入前刷新内存视图：即使写入本身失败，该授予在本
		// 进程中依然有效。
		c.entries = entries
		return c.writeGrantsLocked(entries)
	})
}

// IsAllowed 报告 key 是否有未过期的授予。过期授予会被
// 移除，并在持久化时于返回 false 前重写到磁盘。当
// 缓存不可信时，它总是返回 false。
func (c *TTLCache) IsAllowed(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failed {
		return false
	}
	entry, ok := c.entries[key]
	if !ok {
		return false
	}
	if c.now().Before(entry.ExpiresAt) {
		return true
	}
	// 内存中的副本已过期。对于持久化缓存，另一个进程可能在本调用
	// 等待跨进程锁期间续期了同一条授予。在决定前重新读取磁盘上的最新
	// 集合：只有当新条目也过期时才删除。
	// delete when the fresh entry is expired too. An unconditional delete here
	// would erase a grant that is still live, so neither this process nor a
	// freshly started one would honour it.
	if c.dir == "" {
		delete(c.entries, key)
		return false
	}
	allowed := false
	_ = c.withGrantLock(func() error {
		entries, dirty, err := c.readGrantsLocked()
		if err != nil {
			c.failed = true
			return err
		}
		fresh, ok := entries[key]
		if ok && c.now().Before(fresh.ExpiresAt) {
			allowed = true
		} else if ok {
			delete(entries, key)
			dirty = true
		}
		c.entries = entries
		if !dirty {
			return nil
		}
		return c.writeGrantsLocked(entries)
	})
	return allowed
}

// Deny removes any grant for key. Denials are never persisted: the remaining
// grants are rewritten, but no "denied" record is ever created.
func (c *TTLCache) Deny(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dir == "" {
		delete(c.entries, key)
		return
	}
	if c.failed {
		return
	}
	_ = c.withGrantLock(func() error {
		entries, _, err := c.readGrantsLocked()
		if err != nil {
			c.failed = true
			return err
		}
		if _, ok := entries[key]; !ok {
			c.entries = entries
			return nil
		}
		delete(entries, key)
		c.entries = entries
		return c.writeGrantsLocked(entries)
	})
}

// Load reads <dir>/grants.json and keeps only unexpired grants; expired ones
// are purged from both memory and the file. It is a no-op for a memory-only
// cache and treats a missing file as success. A read/decode failure returns an
// error and marks the cache untrusted (fail-closed); a later successful Load
// clears that state.
func (c *TTLCache) Load() error {
	if c.dir == "" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.withGrantLock(func() error {
		entries, dirty, err := c.readGrantsLocked()
		if err != nil {
			c.failed = true
			return err
		}
		c.entries = entries
		c.failed = false
		if dirty {
			return c.writeGrantsLocked(entries)
		}
		return nil
	})
}

// withGrantLock runs fn while holding the cross-process lock on the sidecar
// file. The caller must hold c.mu. It is a no-op for a memory-only cache.
func (c *TTLCache) withGrantLock(fn func() error) error {
	if c.dir == "" {
		return fn()
	}
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		return fmt.Errorf("security: create ttl grants dir: %w", err)
	}
	lk, err := ttlAcquireLock(filepath.Join(c.dir, ttlGrantsLockFile))
	if err != nil {
		return err
	}
	defer lk.Release()
	return fn()
}

// readGrantsLocked reads the persisted grant set and returns a fresh map with
// expired entries dropped and readable context re-sanitized. dirty reports
// whether the file representation differs from the returned map and therefore
// ought to be rewritten. A missing file is an empty, clean set; a read or
// decode error is returned so callers can fail closed. The caller holds the
// cross-process lock and c.mu.
func (c *TTLCache) readGrantsLocked() (map[string]ttlEntry, bool, error) {
	data, err := os.ReadFile(filepath.Join(c.dir, ttlGrantsFile))
	if errors.Is(err, os.ErrNotExist) {
		return make(map[string]ttlEntry), false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("security: read ttl grants: %w", err)
	}
	var loaded []ttlEntry
	if err := json.Unmarshal(data, &loaded); err != nil {
		return nil, false, fmt.Errorf("security: decode ttl grants: %w", err)
	}
	now := c.now()
	entries := make(map[string]ttlEntry, len(loaded))
	dirty := false
	for _, e := range loaded {
		if e.Key == "" || !now.Before(e.ExpiresAt) {
			dirty = true
			continue
		}
		// Re-sanitize on load so a hand-edited file cannot smuggle a secret in.
		clean := ttlSafeContext(e.Readable)
		if clean != e.Readable {
			dirty = true
		}
		e.Readable = clean
		entries[e.Key] = e
	}
	if capGrantEntries(entries) {
		dirty = true
	}
	return entries, dirty, nil
}

// writeGrantsLocked atomically writes the whole grant set as a sorted JSON
// array. The caller holds the cross-process lock and c.mu.
func (c *TTLCache) writeGrantsLocked(entries map[string]ttlEntry) error {
	list := make([]ttlEntry, 0, len(entries))
	for _, e := range entries {
		list = append(list, e)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Key < list[j].Key })
	data, err := json.Marshal(list)
	if err != nil {
		return fmt.Errorf("security: encode ttl grants: %w", err)
	}
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		return fmt.Errorf("security: create ttl grants dir: %w", err)
	}
	tmp, err := os.CreateTemp(c.dir, "grants-*.tmp")
	if err != nil {
		return fmt.Errorf("security: create ttl grants temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("security: chmod ttl grants temp: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("security: write ttl grants temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("security: sync ttl grants temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("security: close ttl grants temp: %w", err)
	}
	if err := os.Rename(tmpName, filepath.Join(c.dir, ttlGrantsFile)); err != nil {
		return fmt.Errorf("security: rename ttl grants: %w", err)
	}
	return nil
}

// newTTLGrant builds a grant entry with sanitized, bounded readable context.
func newTTLGrant(key, readable string, expires time.Time) ttlEntry {
	return ttlEntry{Key: key, Readable: ttlSafeContext(readable), ExpiresAt: expires}
}

// purgeGrantEntries drops grants whose expiry is not in the future.
func purgeGrantEntries(entries map[string]ttlEntry, now time.Time) bool {
	removed := false
	for key, e := range entries {
		if !now.Before(e.ExpiresAt) {
			delete(entries, key)
			removed = true
		}
	}
	return removed
}

// capGrantEntries evicts the soonest-to-expire grants when the set grows past
// maxTTLGrantEntries. Ordering by expiry keeps eviction deterministic and
// drops the least-trusted grants first.
func capGrantEntries(entries map[string]ttlEntry) bool {
	if len(entries) <= maxTTLGrantEntries {
		return false
	}
	type expiring struct {
		key string
		at  time.Time
	}
	all := make([]expiring, 0, len(entries))
	for key, e := range entries {
		all = append(all, expiring{key: key, at: e.ExpiresAt})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].at.Equal(all[j].at) {
			return all[i].key < all[j].key
		}
		return all[i].at.Before(all[j].at)
	})
	for _, e := range all[:len(all)-maxTTLGrantEntries] {
		delete(entries, e.key)
	}
	return true
}

// ttlSafeContext strips control bytes, redacts credential-shaped spans and
// caps the result on a rune boundary so a persisted context can never carry a
// secret or an unbounded blob.
func ttlSafeContext(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			i++
			continue
		}
		if unicode.IsControl(r) {
			b.WriteByte(' ')
		} else {
			b.WriteRune(r)
		}
		i += size
	}
	return ttlRuneHead(Redact(b.String()), maxTTLReadableRunes)
}

// ttlRuneHead returns the first n runes of s without splitting a UTF-8
// sequence.
func ttlRuneHead(s string, n int) string {
	if n <= 0 {
		return ""
	}
	seen := 0
	for pos := range s {
		if seen == n {
			return s[:pos]
		}
		seen++
	}
	return s
}
