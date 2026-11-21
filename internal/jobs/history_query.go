package jobs

import "sync"

// ExecRecord 描述一次命令执行的历史记录。全部为值字段，便于按值复制与
// 并发共享，不涉及任何 I/O 或外部资源。
type ExecRecord struct {
	Command    string
	StartedMS  int64
	ExitCode   int
	DurationMS int64
}

// ExecHistory 保存最近若干次命令执行记录的有限历史：一个带锁的容量受限
// 环形缓冲。写满后继续 Add 会覆盖最旧的一条，使历史始终保留最近的
// capacity 条。所有方法由读写锁保护，可被多个 goroutine 并发调用。
//
// ExecHistory 是零 I/O、零调用方的可观测辅助，不改变任何现有作业行为。
// Add 按值保存记录；Query/Failed 返回独立切片副本，调用方修改返回值不会
// 影响历史。零值 ExecHistory 可直接调用各方法（容量按 1 处理）。
type ExecHistory struct {
	mu       sync.RWMutex
	buf      []ExecRecord // 长度固定为 capacity；head 为最旧元素下标
	head     int          // 最旧元素在 buf 中的下标
	count    int          // 当前保存的记录数
	capacity int          // 环形缓冲容量
}

// NewExecHistory 构造容量为 capacity 的执行历史。
//
// capacity 小于 1 时按 1 处理（至少保留最近一条记录），因此传入 0 或负数
// 也是合法的。构造出的历史初始为空，各查询方法均返回零值或 nil。
func NewExecHistory(capacity int) *ExecHistory {
	if capacity < 1 {
		capacity = 1
	}
	return &ExecHistory{
		buf:      make([]ExecRecord, capacity),
		capacity: capacity,
	}
}

// Add 追加一条执行记录。缓冲未满时按加入顺序追加，写满后该位置恰为 head，
// 新记录覆盖最旧的一条。r 按值保存，调用方后续修改 r 不会影响已保存的历史。
func (h *ExecHistory) Add(r ExecRecord) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// 兼容未通过构造函数创建的零值结构体。
	if len(h.buf) == 0 {
		if h.capacity < 1 {
			h.capacity = 1
		}
		h.buf = make([]ExecRecord, h.capacity)
	}

	pos := (h.head + h.count) % len(h.buf)
	h.buf[pos] = r
	if h.count == len(h.buf) {
		h.head = (h.head + 1) % len(h.buf)
	} else {
		h.count++
	}
}

// Query 按时间窗过滤历史，返回 StartedMS 落在闭区间 [minMS, maxMS] 内的
// 记录副本，按加入顺序（最旧→最新）稳定排列。minMS 大于 maxMS 时返回 nil。
// 历史为空或无匹配时返回 nil；返回值与历史不共享底层数据。
func (h *ExecHistory) Query(minMS, maxMS int64) []ExecRecord {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if h.count == 0 || minMS > maxMS {
		return nil
	}
	var out []ExecRecord
	for i := 0; i < h.count; i++ {
		r := h.buf[(h.head+i)%len(h.buf)]
		if r.StartedMS >= minMS && r.StartedMS <= maxMS {
			out = append(out, r)
		}
	}
	return out
}

// Failed 返回所有退出码非零的记录副本，按加入顺序（最旧→最新）稳定排列。
// 历史为空或没有失败记录时返回 nil；返回值与历史不共享底层数据。
func (h *ExecHistory) Failed() []ExecRecord {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if h.count == 0 {
		return nil
	}
	var out []ExecRecord
	for i := 0; i < h.count; i++ {
		r := h.buf[(h.head+i)%len(h.buf)]
		if r.ExitCode != 0 {
			out = append(out, r)
		}
	}
	return out
}

// Stats 汇总当前保留的全部历史记录：total 为记录总数，failed 为退出码非零
// 的记录数，avgDurationMS 为 DurationMS 的整数平均值（向零截断）；历史为空
// 时三者均为零。本方法在单个读锁内取一致快照，期间的历史变化不影响本次计算。
func (h *ExecHistory) Stats() (total, failed int, avgDurationMS int64) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if h.count == 0 {
		return 0, 0, 0
	}
	var sum int64
	for i := 0; i < h.count; i++ {
		r := h.buf[(h.head+i)%len(h.buf)]
		if r.ExitCode != 0 {
			failed++
		}
		sum += r.DurationMS
	}
	return h.count, failed, sum / int64(h.count)
}
