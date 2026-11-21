package jobs

import (
	"fmt"
	"strings"
	"sync"
)

// SnapshotHistory 保存优先队列快照的有限历史：一个带锁的容量受限环形缓冲。
//
// 它按入队顺序保留最近 capacity 份 PrioritySnapshot：缓冲写满后继续 Push 会
// 覆盖最旧的一份。所有方法由读写锁保护，可被多个 goroutine 并发调用。
//
// SnapshotHistory 是零 I/O、零调用方的可观测辅助，不改变任何现有作业行为。
// Push 会深拷贝入参快照（含 ByPriority 映射与 Head/Tail 切片），Timeline 同样
// 返回深拷贝，因此调用方持有的快照与历史之间不共享任何底层数据。
type SnapshotHistory struct {
	mu       sync.RWMutex
	buf      []PrioritySnapshot // 长度固定为 capacity；head 为最旧元素下标
	head     int                // 最旧元素在 buf 中的下标
	count    int                // 当前保存的快照数
	capacity int                // 环形缓冲容量
}

// NewSnapshotHistory 构造容量为 capacity 的快照历史。
//
// capacity 小于 1 时按 1 处理（至少保留最近一份快照），因此传入 0 或负数
// 也是合法的。构造出的历史初始为空，Timeline 与 Changes 均返回 nil。
func NewSnapshotHistory(capacity int) *SnapshotHistory {
	if capacity < 1 {
		capacity = 1
	}
	return &SnapshotHistory{
		buf:      make([]PrioritySnapshot, capacity),
		capacity: capacity,
	}
}

// Push 追加一份快照。缓冲未满时按入队顺序追加；写满后覆盖最旧的一份，使
// 历史始终保留最近的 capacity 份。s 会被深拷贝，调用方后续修改 s 不会影响
// 已保存的历史。零值 SnapshotHistory 也可直接 Push（容量按 1 处理）。
func (h *SnapshotHistory) Push(s PrioritySnapshot) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// 兼容未通过构造函数创建的零值结构体。
	if len(h.buf) == 0 {
		if h.capacity < 1 {
			h.capacity = 1
		}
		h.buf = make([]PrioritySnapshot, h.capacity)
	}

	// 未满时写入队尾；已满时该位置恰为 head，新元素覆盖最旧元素。
	pos := (h.head + h.count) % len(h.buf)
	h.buf[pos] = clonePrioritySnapshot(s)
	if h.count == len(h.buf) {
		h.head = (h.head + 1) % len(h.buf)
	} else {
		h.count++
	}
}

// Timeline 按入队顺序（最旧→最新）返回所有现存快照的独立副本。
//
// 返回切片及其内部的 ByPriority 映射、Head/Tail 切片均与历史不共享底层数据；
// 调用方修改返回值不会影响历史。历史为空时返回 nil。
func (h *SnapshotHistory) Timeline() []PrioritySnapshot {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if h.count == 0 {
		return nil
	}
	out := make([]PrioritySnapshot, 0, h.count)
	for i := 0; i < h.count; i++ {
		idx := (h.head + i) % len(h.buf)
		out = append(out, clonePrioritySnapshot(h.buf[idx]))
	}
	return out
}

// Changes 返回相邻快照之间的 ID 变化摘要，每一对相邻快照对应一项（按时间顺序）。
//
// 比较复用 QueueDelta 的语义：只按 ID 判断新增/移除，同一 ID 的 Priority 或
// Seq 改变不算变化。ID 取自快照的 Head 与 Tail 可见条目（Head 在前、Tail 在
// 后，QueueDelta 会按 ID 去重，故二者重叠无碍）。摘要为稳定中文，形如
//
//	第1→2份快照：新增[乙、甲]；移除[旧]
//
// 没有新增或移除的一侧写作“无”。本方法只读历史、纯计算，不修改任何状态；
// 快照数不足两份时返回 nil。
func (h *SnapshotHistory) Changes() []string {
	// 借助 Timeline 在单个读锁内取得一致副本，期间历史变化不影响本次计算。
	timeline := h.Timeline()
	if len(timeline) < 2 {
		return nil
	}
	out := make([]string, 0, len(timeline)-1)
	for i := 0; i+1 < len(timeline); i++ {
		beforeItems := snapshotVisibleItems(timeline[i])
		afterItems := snapshotVisibleItems(timeline[i+1])
		added, removed := QueueDelta(beforeItems, afterItems)
		out = append(out, fmt.Sprintf("第%d→%d份快照：新增[%s]；移除[%s]",
			i+1, i+2, joinChangeIDs(added), joinChangeIDs(removed)))
	}
	return out
}

// clonePrioritySnapshot 深拷贝一份快照：ByPriority 映射与 Head/Tail 切片均
// 重新分配，返回值与入参不共享任何底层数据。
func clonePrioritySnapshot(s PrioritySnapshot) PrioritySnapshot {
	out := PrioritySnapshot{Total: s.Total}
	if s.ByPriority != nil {
		out.ByPriority = make(map[int]int, len(s.ByPriority))
		for prio, n := range s.ByPriority {
			out.ByPriority[prio] = n
		}
	}
	out.Head = copyPriorityItems(s.Head)
	out.Tail = copyPriorityItems(s.Tail)
	return out
}

// copyPriorityItems 复制条目切片，保留 nil 与非 nil 空切片的区分。
func copyPriorityItems(items []PriorityItem) []PriorityItem {
	if items == nil {
		return nil
	}
	out := make([]PriorityItem, len(items))
	copy(out, items)
	return out
}

// snapshotVisibleItems 汇总快照中可见的条目（Head 在前、Tail 在后）。二者
// 可能重叠，但 QueueDelta 会按 ID 去重，不影响变化判定。
func snapshotVisibleItems(s PrioritySnapshot) []PriorityItem {
	items := make([]PriorityItem, 0, len(s.Head)+len(s.Tail))
	items = append(items, s.Head...)
	items = append(items, s.Tail...)
	return items
}

// joinChangeIDs 将 ID 列表用中文顿号连接；空列表写作“无”。
func joinChangeIDs(ids []string) string {
	if len(ids) == 0 {
		return "无"
	}
	return strings.Join(ids, "、")
}
