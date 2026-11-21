package jobs

import (
	"sort"
	"sync"
)

// priorityLess 实现与 SelectByPriority 一致的排序规则：Priority 降序、
// Seq 升序。当两者都相等时返回 false（视为等价），从而让稳定插入把新元素
// 放在已有等价元素之后，保持入队顺序。
func priorityLess(a, b PriorityItem) bool {
	if a.Priority != b.Priority {
		return a.Priority > b.Priority
	}
	return a.Seq < b.Seq
}

// priorityQueue 是基于切片的稳定优先队列。内部始终维持按 (Priority 降序,
// Seq 升序) 排序的切片，Pop 从队首取出最高优先级条目。
//
// 它是零调用方的可观测辅助：不接入 Registry，也不改变任何现有作业行为。
// 所有方法由读写锁保护，可被多个 goroutine 并发调用。
type priorityQueue struct {
	mu    sync.RWMutex
	items []PriorityItem
}

// PriorityQueue 用给定的初始条目构造优先队列。
//
// 构造过程是纯函数式的：先复制 items 再排序，绝不修改入参，返回队列的内部
// 切片与 items 不共享底层数组。items 为 nil 或空时返回空队列。
func PriorityQueue(items []PriorityItem) *priorityQueue {
	q := &priorityQueue{}
	if len(items) == 0 {
		return q
	}
	q.items = make([]PriorityItem, len(items))
	copy(q.items, items)
	sort.SliceStable(q.items, func(i, j int) bool {
		return priorityLess(q.items[i], q.items[j])
	})
	return q
}

// Push 插入一个条目并保持队列有序。等价条目（Priority 与 Seq 均相同）会
// 排在同键已有条目之后，从而保留插入顺序（稳定语义）。
func (q *priorityQueue) Push(item PriorityItem) {
	q.mu.Lock()
	defer q.mu.Unlock()

	// 找到第一个“新条目严格优于现有条目”的位置并插入；等价条目不满足
	// less，因此会被跳过，新条目落到它们之后。
	idx := sort.Search(len(q.items), func(i int) bool {
		return priorityLess(item, q.items[i])
	})
	q.items = append(q.items, PriorityItem{})
	copy(q.items[idx+1:], q.items[idx:])
	q.items[idx] = item
}

// Pop 取出并返回队首（最高优先级）条目。队列为空时返回零值与 false。
func (q *priorityQueue) Pop() (PriorityItem, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if len(q.items) == 0 {
		return PriorityItem{}, false
	}
	item := q.items[0]
	// 将队首置零后再右移，避免对已取出元素的引用滞留。
	q.items[0] = PriorityItem{}
	q.items = q.items[1:]
	return item, true
}

// Len 返回当前队列中的条目数。
func (q *priorityQueue) Len() int {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return len(q.items)
}

// Snapshot 返回队列当前内容的有序副本。返回值与内部切片不共享底层数组，
// 调用方修改副本不会影响队列。队列为空时返回 nil。
func (q *priorityQueue) Snapshot() []PriorityItem {
	q.mu.RLock()
	defer q.mu.RUnlock()

	if len(q.items) == 0 {
		return nil
	}
	out := make([]PriorityItem, len(q.items))
	copy(out, q.items)
	return out
}

// SortedByPriority 是 SelectByPriority 在 limit 取全量时的别名，返回按
// (Priority 降序, Seq 升序) 稳定排序后的全部条目。它是纯函数，不修改入参。
func SortedByPriority(items []PriorityItem) []PriorityItem {
	return SelectByPriority(items, len(items))
}

// PriorityOf 返回首个 ID 匹配条目的优先级。找不到时返回零值与 false。
// 它是纯函数，不修改入参，也不读取任何全局状态。
func PriorityOf(id string, items []PriorityItem) (int, bool) {
	for _, item := range items {
		if item.ID == id {
			return item.Priority, true
		}
	}
	return 0, false
}
