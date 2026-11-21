package mcp

import "sync"

// defaultHealthHistoryCapacity 是 HealthHistory 的默认环形缓冲容量。
// 容量在构造时固定，写入超过容量后最旧记录被覆盖，内存占用不随 Record 次数增长。
const defaultHealthHistoryCapacity = 64

// HealthHistory 在内存中保留最近若干条健康快照，是一个固定容量的环形缓冲。
// 它把 HealthTracker 的离散快照串成有界历史，供 Recent 回看、UnhealthyStreak
// 统计尾部连续失败——典型用途是在连续失败达到阈值前避免告警抖动。
//
// 零值可直接使用：首次 Record 时按默认容量惰性分配。它只依赖标准库、不做
// 任何 I/O，也不接入现有连接流程；刻意保持零调用方。
//
// buf 长度在初始化后恒等于 capacity；start 为当前最旧记录的下标，count 为
// 有效记录数（0 <= count <= capacity）。mu 保护以上全部字段。
type HealthHistory struct {
	mu       sync.RWMutex
	buf      []HealthSnapshot
	capacity int
	start    int
	count    int
}

// NewHealthHistory 构造一个固定容量的历史环形缓冲。capacity <= 0 时改用
// 默认容量 64。底层切片一次性分配，之后不再增长。
func NewHealthHistory(capacity int) *HealthHistory {
	if capacity <= 0 {
		capacity = defaultHealthHistoryCapacity
	}
	return &HealthHistory{
		buf:      make([]HealthSnapshot, capacity),
		capacity: capacity,
	}
}

// Record 追加一条快照。缓冲未满时写到当前尾部；已满时覆盖最旧记录并把
// start 前移一位，因此容量恒定、内存占用不随调用次数增长。快照按值拷贝，
// 与调用方后续对原变量的修改完全隔离。
func (h *HealthHistory) Record(snap HealthSnapshot) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.initLocked()
	idx := (h.start + h.count) % h.capacity
	h.buf[idx] = snap
	if h.count < h.capacity {
		h.count++
	} else {
		h.start = (h.start + 1) % h.capacity
	}
}

// Recent 返回最近 n 条快照的副本，按记录时间从旧到新排列。
// n <= 0 或空历史时返回非 nil 的空切片；n 大于已记录条数时返回全部记录。
// 返回的切片与内部缓冲无别名，调用方可自由修改。
func (h *HealthHistory) Recent(n int) []HealthSnapshot {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if n <= 0 || h.count == 0 {
		return []HealthSnapshot{}
	}
	m := n
	if m > h.count {
		m = h.count
	}
	out := make([]HealthSnapshot, m)
	oldest := (h.start + h.count - m) % h.capacity
	for i := 0; i < m; i++ {
		out[i] = h.buf[(oldest+i)%h.capacity]
	}
	return out
}

// UnhealthyStreak 返回末尾连续不健康记录的条数：从最新一条向前数，直到
// 遇到健康记录或到达历史起点为止。空历史返回 0；若全部记录均不健康，
// 返回已记录的条数（上限为容量）。
func (h *HealthHistory) UnhealthyStreak() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	streak := 0
	for i := h.count - 1; i >= 0; i-- {
		if h.buf[(h.start+i)%h.capacity].Healthy {
			break
		}
		streak++
	}
	return streak
}

// initLocked 为零值历史惰性分配默认容量的缓冲。调用方须持有 h.mu。
func (h *HealthHistory) initLocked() {
	if h.buf == nil {
		h.capacity = defaultHealthHistoryCapacity
		h.buf = make([]HealthSnapshot, h.capacity)
	}
}
