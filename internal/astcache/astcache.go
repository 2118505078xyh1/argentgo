// Package astcache 为 go/parser 的解析结果提供内容寻址的进程级缓存。
//
// 动机（学自 oh-my-pi crates/ast/src/parse_cache.rs）：一次全文件 parse 在
// 81KB 上约 13.5ms、1MB 上约 187ms，而 ast_edit / repo_map / 语法劣化检查
// 的其余工作都 <1ms。同一批大 Go 文件在多轮工具调用中被反复解析，解析本身
// 才是热点。缓存 *ast.File + *token.FileSet，而非缓存最终编辑结果，使多个
// 消费者都能受益。
//
// 本包纯标准库、无 cgo、无 I/O、无全局可变状态污染：所有状态封装在 Cache
// 实例中（默认实例供进程级使用，测试可建独立实例）。缓存 key 用 sha256 摘要
// + 长度 + 语言标签；命中仍逐字节比对源（哈希碰撞只导致重解析，不产生错误结果）。
// 内存按「源字节×16 + 节点数×64」估算计费（对齐竞品成本模型），而非按源长度，
// 因为 AST 的内存占用远大于源码。
package astcache

import (
	"crypto/sha256"
	"go/ast"
	"go/parser"
	"go/token"
	"sync"
)

const (
	// DefaultMaxBytes 是 AST 缓存的默认内存预算（估算值）。
	DefaultMaxBytes = 64 << 20 // 64 MiB
	// DefaultMaxEntries 限制条目数，避免大量小文件耗尽元数据。
	DefaultMaxEntries = 12
	// bytesPerSourceByte / bytesPerNode 是 AST 内存估算系数。
	bytesPerSourceByte = 16
	bytesPerNode       = 64
)

// key 是内容寻址的缓存键。摘要与长度共同定位，语言标签区分不同解析器。
type key struct {
	sum  [sha256.Size]byte
	len  int
	lang string
}

// entry 是一条缓存记录。size 是估算的 AST 内存占用。
type entry struct {
	file *ast.File
	fset *token.FileSet
	src  []byte // 命中后逐字节比对，防哈希碰撞
	size int
	// prev 是上一版本（供调用方做增量重解析参考），不参与 LRU 计费主体。
	prev *entry
}

// Options 配置缓存容量。零值使用默认值。
type Options struct {
	MaxBytes   int
	MaxEntries int
}

// Cache 是并发安全的 AST 解析缓存。
type Cache struct {
	mu         sync.Mutex
	entries    map[key]*entry
	order      []key // LRU：最旧在前
	used       int
	maxBytes   int
	maxEntries int
	// byPath 记录每个路径最近一次解析的条目，供内容变化后保留上一版本。
	byPath map[string]*entry
}

var defaultCache = New(Options{})

// New 创建一个缓存实例。
func New(opts Options) *Cache {
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = DefaultMaxBytes
	}
	if opts.MaxEntries <= 0 {
		opts.MaxEntries = DefaultMaxEntries
	}
	return &Cache{
		entries:    make(map[key]*entry),
		maxBytes:   opts.MaxBytes,
		maxEntries: opts.MaxEntries,
		byPath:     make(map[string]*entry),
	}
}

// Default 返回进程级默认缓存实例。
func Default() *Cache { return defaultCache }

// Parse 解析 src。path 仅用于 ticket fencing（可为空），lang 区分解析器语言。
// 命中缓存时返回缓存的 *ast.File 与 *token.FileSet；否则调用 parser.ParseFile
// 并缓存结果。mode 透传给 parser.ParseFile，参与缓存 key（不同 mode 的 AST 不同）。
func (c *Cache) Parse(path, lang string, src []byte, mode parser.Mode) (*ast.File, *token.FileSet, error) {
	k := makeKey(lang, src, mode)

	c.mu.Lock()
	if e := c.entries[k]; e != nil && bytesEqual(e.src, src) {
		c.touchLocked(k)
		c.mu.Unlock()
		return e.file, e.fset, nil
	}
	c.mu.Unlock()

	// 在锁外解析，避免阻塞其它 key 的命中。
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, mode)

	if err == nil || file != nil {
		e := &entry{file: file, fset: fset, src: append([]byte(nil), src...)}
		e.size = estimateSize(len(src), file)
		c.mu.Lock()
		if old := c.entries[k]; old != nil {
			c.used -= old.size
			delete(c.entries, k)
			c.removeFromOrderLocked(k)
		}
		// 保留同一路径的上一版本供增量重解析参考（内容变化时 key 不同，
		// 无法用 key 关联，必须按 path 记录）。
		if path != "" {
			if prev := c.byPath[path]; prev != nil && prev != e {
				e.prev = prev
			}
			c.byPath[path] = e
		}
		c.entries[k] = e
		c.used += e.size
		c.order = append(c.order, k)
		c.evictLocked()
		c.mu.Unlock()
	}

	return file, fset, err
}

// ParseDefault 使用默认缓存实例解析。
func ParseDefault(path, lang string, src []byte, mode parser.Mode) (*ast.File, *token.FileSet, error) {
	return defaultCache.Parse(path, lang, src, mode)
}

// makeKey 生成内容寻址键。mode 编进 lang 标签，避免不同 mode 共享同一条目。
func makeKey(lang string, src []byte, mode parser.Mode) key {
	return key{
		sum:  sha256.Sum256(src),
		len:  len(src),
		lang: lang + "|" + parserModeTag(mode),
	}
}

// parserModeTag 把 parser.Mode 映射为稳定标签（只需区分影响 AST 的关键位）。
func parserModeTag(mode parser.Mode) string {
	tag := ""
	if mode&parser.ParseComments != 0 {
		tag += "c"
	}
	if mode&parser.SkipObjectResolution != 0 {
		tag += "s"
	}
	if mode&parser.AllErrors != 0 {
		tag += "e"
	}
	return tag
}

// estimateSize 估算 AST 内存占用。节点数通过 ast.Inspect 统计（有上限，避免
// 超大文件统计本身成为热点）。
func estimateSize(srcLen int, f *ast.File) int {
	size := srcLen * bytesPerSourceByte
	if f != nil {
		nodes := 0
		ast.Inspect(f, func(ast.Node) bool {
			nodes++
			return true
		})
		if nodes > 100000 {
			nodes = 100000 // 统计上限
		}
		size += nodes * bytesPerNode
	}
	return size
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (c *Cache) touchLocked(k key) {
	for i, kk := range c.order {
		if kk == k {
			c.order = append(c.order[:i], c.order[i+1:]...)
			c.order = append(c.order, k)
			return
		}
	}
}

func (c *Cache) removeFromOrderLocked(k key) {
	for i, kk := range c.order {
		if kk == k {
			c.order = append(c.order[:i], c.order[i+1:]...)
			return
		}
	}
}

// evictLocked 按 LRU 驱逐，直到满足条目数与字节预算。
func (c *Cache) evictLocked() {
	for len(c.order) > c.maxEntries || (c.used > c.maxBytes && len(c.order) > 0) {
		oldest := c.order[0]
		c.order = c.order[1:]
		if e := c.entries[oldest]; e != nil {
			c.used -= e.size
			delete(c.entries, oldest)
		}
	}
}

// Stats 返回缓存统计（供诊断）。
type Stats struct {
	Entries    int
	UsedBytes  int
	MaxBytes   int
	MaxEntries int
}

func (c *Cache) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Stats{
		Entries:    len(c.entries),
		UsedBytes:  c.used,
		MaxBytes:   c.maxBytes,
		MaxEntries: c.maxEntries,
	}
}

// Reset 清空缓存（测试用）。
func (c *Cache) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[key]*entry)
	c.order = nil
	c.used = 0
	c.byPath = make(map[string]*entry)
}
