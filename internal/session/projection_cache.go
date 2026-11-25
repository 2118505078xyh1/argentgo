package session

// ProjectionCache 为投影提供 generation stamp 失效与可选的 O(新增) 增量（#187）。
//
// 投影（records → entries）通常是"纯函数"，但并非总是"逐条独立"：会话的
// messageEntries 会在整段上做工具调用配对、上下文编辑与拒答过滤。对这类投影，
// 把新尾部的投影拼到旧前缀结果后面是**错误**的——一个早前的 tool_call 可能在
// 尾部才配对成功，旧结果里已经插入的合成结果不会被撤销。
//
// 因此本缓存有两种模式：
//   - 默认（Incremental=false）：stamp 完全相同时复用整段结果，任何变化都整段重算。
//     它保证跨记录状态的投影逐字节等价，适合 messageEntries。
//   - Incremental=true：stamp 是真前缀时只投影新增尾部并追加，要求 project 对每个
//     前缀独立（例如逐条 id 映射）。调用方必须自己确认这一前提。
//
// 它是纯原语：不读磁盘、不读时钟、不碰全局状态；调用方持有实例并负责并发。
type ProjectionCache struct {
	// Incremental 仅当注入的 project 逐条独立时置 true；默认整段模式对任何投影安全。
	Incremental bool
	stamp       []recordStamp
	entries     []Entry
}

type recordStamp struct {
	ID   string
	Kind string
}

// Project 返回 records 投影后的 entries。命中条件由 Incremental 决定。
// project 由调用方注入，避免本包循环依赖。
func (c *ProjectionCache) Project(records []Record, project func([]Record) []Entry) []Entry {
	if project == nil {
		return nil
	}
	n := len(records)
	stamp := make([]recordStamp, n)
	for i, record := range records {
		stamp[i] = recordStamp{ID: record.ID, Kind: record.Kind}
	}
	shared := prefixLen(c.stamp, stamp)

	// 增量模式：缓存 stamp 是当前 stamp 的真前缀（可以更短），只投影新增尾部。
	if c.Incremental && shared == len(c.stamp) && len(c.stamp) <= n && len(c.entries) == len(c.stamp) {
		out := make([]Entry, len(c.entries))
		copy(out, c.entries)
		if shared < n {
			tail := project(records[shared:])
			out = append(out, tail...)
		}
		c.stamp = stamp
		c.entries = out
		return out
	}

	// 整段模式（或前缀失效）：stamp 逐项相同时复用整段副本，否则整段重算。
	// 返回副本，避免调用方修改 slice 污染下次命中。
	if shared == n && len(c.stamp) == n {
		out := make([]Entry, len(c.entries))
		copy(out, c.entries)
		return out
	}
	out := project(records)
	c.stamp = stamp
	c.entries = out
	return out
}

// prefixLen 返回 a 作为 b 前缀时相等的元素数；a 比 b 长时最多返回 len(b)。
func prefixLen(a, b []recordStamp) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

// Generation 返回当前缓存覆盖的记录数（generation stamp）。
func (c *ProjectionCache) Generation() int { return len(c.stamp) }

// Reset 清空缓存。
func (c *ProjectionCache) Reset() {
	c.stamp = nil
	c.entries = nil
}
