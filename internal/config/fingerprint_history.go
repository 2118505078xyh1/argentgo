package config

import "sync"

// FingerprintHistory 是一段有界的历史指纹序列：它按时间顺序保留最近
// capacity 个非空指纹，并去掉相邻的重复项。它只做内存中的记录与查询，
// 不读写磁盘、不启动 goroutine，也不持有对任何外部对象的引用。
//
// 内部用一个长度等于 capacity 的切片实现环形缓冲：head 指向下一次写入
// 的位置，size 是当前有效条目数。所有方法都在互斥锁下访问这些字段，因此
// 同一个 *FingerprintHistory 可被多个 goroutine 并发使用。
//
// 零值是可用状态：它的缓冲为空，Record 不记录任何指纹，Recent 返回空
// 切片，ChangedInLastN 返回 false。NewFingerprintHistory 对 capacity <= 0
// 也返回同样的空历史（而不是隐式选择一个默认值），以免调用方对容量产生
// 误解。默认没有调用点，仅作为快照校验等场景按需使用的基础原语。
type FingerprintHistory struct {
	mu   sync.RWMutex
	buf  []string // 环形缓冲，长度为容量；未满时有效条目位于 [0, size)
	head int      // 下一次写入位置
	size int      // 当前有效条目数，始终 <= len(buf)
}

// NewFingerprintHistory 构造一个容量为 capacity 的空历史。
// capacity <= 0 时得到一个不记录任何指纹的空历史（见 FingerprintHistory）。
func NewFingerprintHistory(capacity int) *FingerprintHistory {
	if capacity < 0 {
		capacity = 0
	}
	return &FingerprintHistory{buf: make([]string, capacity)}
}

// Record 追加一个非空指纹。
//
// 空字符串被忽略（与 Snapshot.Fingerprint 的哨兵语义一致），且不会影响
// 相邻去重的状态。若新指纹与当前最后一条相同，则跳过，避免无变化轮询
// 在历史里堆积重复项。缓冲写满后，最旧的一条被覆盖。
func (h *FingerprintHistory) Record(fp string) {
	if fp == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()

	cap := len(h.buf)
	if cap == 0 {
		return
	}
	// 最后一条的位置：未满时在 size-1，写满后在 head-1（统一用取模表示）。
	if h.size > 0 && h.buf[(h.head-1+cap)%cap] == fp {
		return
	}

	h.buf[h.head] = fp
	h.head = (h.head + 1) % cap
	if h.size < cap {
		h.size++
	}
}

// Recent 返回最近 n 条指纹的副本，顺序为从旧到新。
//
// n <= 0 或历史为空时返回空切片；n 超过条目数时只返回已有的全部条目。
// 返回值总是新分配的切片，调用方任意修改都不会影响内部缓冲。
func (h *FingerprintHistory) Recent(n int) []string {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if n <= 0 || h.size == 0 {
		return []string{}
	}
	if n > h.size {
		n = h.size
	}
	cap := len(h.buf)
	// 最近 n 条中最旧的一条位于 head-n（取模），随后按写入顺序展开。
	start := (h.head - n + cap) % cap
	out := make([]string, n)
	for i := range out {
		out[i] = h.buf[(start+i)%cap]
	}
	return out
}

// ChangedInLastN 报告最近 n 条指纹中是否出现过不同的指纹。
//
// 窗口内条目少于两条时返回 false；只要存在任意两条不相同的指纹即返回
// true（相邻去重只保证相邻项不同，非相邻位置仍可能重复，因此仍需实际
// 比较而不是只数条目数）。
func (h *FingerprintHistory) ChangedInLastN(n int) bool {
	recent := h.Recent(n)
	if len(recent) < 2 {
		return false
	}
	first := recent[0]
	for _, fp := range recent[1:] {
		if fp != first {
			return true
		}
	}
	return false
}
