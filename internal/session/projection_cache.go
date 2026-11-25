package session

// ProjectionCache 为纯函数投影提供 O(新增) 增量与 generation stamp 失效（#187）。
//
// 投影（records → entries）是纯函数，但每轮 messages() 都从头重算整段历史，
// 长会话成本随总长度线性增长。本缓存只保留"已投影前缀"的 stamp 与结果：
// 当新调用的 records 以同一前缀开头（记录数更多、前缀逐项 id/kind 相同）时，
// 只投影新增尾部并追加；任何前缀变化（fork/压缩/改历史）都整段失效重算。
//
// 它是纯原语：不读磁盘、不读时钟、不碰全局状态；调用方持有实例。
// 不直接接入 messages()——读路径字节语义由现有 conformance/golden 锁定，
// 接入点由主会话在确认等价后决定（opt-in）。
type ProjectionCache struct {
	stamp   []recordStamp
	entries []Entry
}

type recordStamp struct {
	ID   string
	Kind string
}

// Project 返回 records 投影后的 entries。命中前缀时只投影新增尾部。
// project 是纯函数（与 messageEntries 同语义），由调用方注入，避免本包循环依赖。
func (c *ProjectionCache) Project(records []Record, project func([]Record) []Entry) []Entry {
	if project == nil {
		return nil
	}
	n := len(records)
	stamp := make([]recordStamp, n)
	for i, record := range records {
		stamp[i] = recordStamp{ID: record.ID, Kind: record.Kind}
	}

	// 前缀匹配：缓存 stamp 必须是当前 stamp 的真前缀（长度更短且逐项相等）。
	shared := prefixLen(c.stamp, stamp)
	if shared == len(c.stamp) && len(c.stamp) <= n {
		// 命中：复用已投影结果，只投影 [shared, n) 的增量尾部。
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

	// 失效（fork/改写/缩短）：整段重算。
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
