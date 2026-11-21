package hooks

import "sync"

// EventDeduplicator 是一个纯内存的并发安全去重原语，用于按 key（例如子 agent
// 的 CallID）保证某个事件"每个 key 只触发一次"。
//
// 设计动机：设计蓝图中"subagent 不重复触发 SubagentStop"要求同一个子 agent
// 的终止事件只 fire 一次。调用方在准备分发前先调用 First(key)，只有返回 true
// 时才真正触发；重复的输入会被静默丢弃。若某个 key 的生命周期结束后又被复用，
// 可调用 Release(key) 清除其记录，使其有资格再次 First。
//
// 它不持有任何全局状态、不产生 I/O、不依赖时钟，也不解析 key 的内容，因此 key
// 可为任意字符串（含 CJK 文本），并可作为零值直接使用——首次写入时惰性初始化
// 内部 map。零值语义让调用方无需显式构造函数即可声明字段或局部变量。
type EventDeduplicator struct {
	mu   sync.Mutex
	seen map[string]struct{}
}

// First 报告 key 是否是首次出现。首次出现时记录该 key 并返回 true；若 key 已
// 被记录（且尚未 Release），则返回 false。
//
// 检查与记录在同一把锁内完成，因此并发调用下同一个 key 必然至多有一个调用方
// 得到 true，其余都得到 false。
func (d *EventDeduplicator) First(key string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.seen[key]; ok {
		return false
	}
	if d.seen == nil {
		d.seen = make(map[string]struct{})
	}
	d.seen[key] = struct{}{}
	return true
}

// Release 清除 key 的记录，使其有资格在之后的 First 调用中再次返回 true。
// 释放一个不存在（或已释放）的 key 是无害的空操作。
func (d *EventDeduplicator) Release(key string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.seen, key)
}

// Len 返回当前已记录、尚未释放的 key 数量。
func (d *EventDeduplicator) Len() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.seen)
}

// Reset 清除所有记录，等价于把所有 key 一次性 Release。
func (d *EventDeduplicator) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.seen = nil
}
