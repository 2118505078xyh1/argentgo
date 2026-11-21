package mcp

import (
	"sync"
	"sync/atomic"
)

// HealthSnapshot 是一次健康观测的不可变快照。字段均为值类型，调用方持有的
// 快照与 tracker 内部状态完全隔离。Gen 是单调增长的版本号，调用方可用
// ChangedSince 做 O(1) 的变更探测（热重载场景）。
type HealthSnapshot struct {
	Healthy   bool   // 最近一次观测是否健康
	ToolCount int    // 工具数；失败时回退到上一次健康值（last-good）
	LastError string // 最近一次失败的错误文本，健康时为 ""
	Gen       uint64 // 单调增长的生成号
}

// HealthTracker 在内存中聚合健康观测并产出快照。零值可直接使用。
// 它只依赖标准库、不做任何 I/O，也不接入现有连接流程；调用方按需 Observe。
//
// mu 保护除 gen 之外的所有字段；gen 用 atomic 以便 ChangedSince 无需加锁。
type HealthTracker struct {
	mu        sync.RWMutex
	healthy   bool
	toolCount int
	lastError string
	lastGood  int
	gen       atomic.Uint64
}

// Observe 记录一次健康观测。当 healthy 为 true 且 err 为 nil 时视为“健康观测”，
// 更新工具数并记为 last-good、清空 LastError；否则视为失败：Healthy 置 false，
// ToolCount 回退到上一次健康值，LastError 记录 err 文本（err 为 nil 时为空）。
// 无论成功失败，Gen 都单调递增一次（即 Gen 计数的是观测次数，而非取值变化）。
func (h *HealthTracker) Observe(healthy bool, toolCount int, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if healthy && err == nil {
		h.healthy = true
		h.toolCount = toolCount
		h.lastGood = toolCount
		h.lastError = ""
	} else {
		h.healthy = false
		h.toolCount = h.lastGood // 失败回退：保留上一次健康工具数
		if err != nil {
			h.lastError = err.Error()
		} else {
			h.lastError = ""
		}
	}
	h.gen.Add(1)
}

// Snapshot 返回当前快照的副本。读操作在 mu 保护下进行，返回的 HealthSnapshot
// 是纯值类型，调用方修改它不会影响 tracker 内部状态。
func (h *HealthTracker) Snapshot() HealthSnapshot {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return HealthSnapshot{
		Healthy:   h.healthy,
		ToolCount: h.toolCount,
		LastError: h.lastError,
		Gen:       h.gen.Load(),
	}
}

// ChangedSince 报告快照自给定生成号以来是否发生变化，即当前 Gen > gen。
// 该探测走原子读、无需加锁，适合热重载的高频轮询路径。传入更大或相等的
// gen（例如来自“未来”的版本）返回 false。
func (h *HealthTracker) ChangedSince(gen uint64) bool {
	return h.gen.Load() > gen
}

// HealthReporter 是可选的只读健康视图。它刻意不进入 Client 接口：
// 大量调用方与测试用 fake 实现 Client，往接口加方法会破坏它们。
// 需要健康信息的调用方按如下方式选择性读取（与包内 Stderr 的接线先例一致）：
//
//	if r, ok := client.(mcp.HealthReporter); ok {
//	    snap := r.HealthSnapshot()
//	}
type HealthReporter interface {
	HealthSnapshot() HealthSnapshot
}

// HealthSnapshot 返回客户端的健康快照。未启用健康跟踪（health 为 nil）时
// 返回零值快照，不分配 tracker、不产生任何 I/O；默认关闭路径逐字节不变。
func (c *client) HealthSnapshot() HealthSnapshot {
	if c.health == nil {
		return HealthSnapshot{}
	}
	return c.health.Snapshot()
}
