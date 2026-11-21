package mcp

import (
	"sort"
	"sync"
)

// HealthAggregator 在内存中维护一组可观测健康源，并在 Report 时把各源快照与
// 连接状态交给 AggregateHealth 聚合成一份 MCPHealthReport。它是健康聚合的
// 生命周期适配原语：调用方在源出现时 Track、在健康变化时 Observe，并在轮次
// 边界通过 PollReporters 从各源的 HealthReporter 拉取最新快照。
//
// 零值可直接使用：默认没有任何源，Report 返回 OverallHealthy=true 的空报告。
// 它只依赖标准库、不做任何 I/O，也不接入现有连接流程；刻意保持零调用方。
//
// 连接状态不额外暴露设置入口，而是由观测到的快照派生：
//   - Track 时置为 ConnDisconnected（已注册但尚未观测）；
//   - Observe/PollReporters 写入快照时，Healthy=true 派生 ConnReady，
//     否则派生 ConnFailed。
//
// 这样 Report 总是能把“快照 + 连接状态”成对交给 AggregateHealth，而不必让
// 调用方维护第二套状态机。
//
// mu 保护 reporters/reports/states 三个 map。注意绝不在持锁期间调用外部
// reporter 的方法，避免回调造成死锁。
type HealthAggregator struct {
	mu        sync.RWMutex
	reporters map[string]HealthReporter
	reports   map[string]HealthSnapshot
	states    map[string]ConnState
}

// Track 注册一个可观测源。重复 Track 同名会覆盖其 reporter，并把快照重置为
// 零值、连接状态重置为 ConnDisconnected——语义等同于“重新注册”。
// rep 为 nil（未挂接可轮询源）时仍然注册名字，但 PollReporters 会跳过它。
func (a *HealthAggregator) Track(name string, rep HealthReporter) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.initLocked()
	a.reporters[name] = rep
	a.reports[name] = HealthSnapshot{}
	a.states[name] = ConnDisconnected
}

// Untrack 移除一个源，同时清除其 reporter、快照与连接状态，使其不再出现在
// 后续 Report 中。名字不存在时为空操作。
func (a *HealthAggregator) Untrack(name string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.reporters, name)
	delete(a.reports, name)
	delete(a.states, name)
}

// Observe 更新某个源的快照，并按快照健康度派生连接状态（见类型注释）。
// 该名字即使尚未 Track 也会记录；后续同名 Track 会将其重置。
func (a *HealthAggregator) Observe(name string, h HealthSnapshot) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.initLocked()
	a.reports[name] = h
	a.states[name] = healthDerivedState(h)
}

// Report 把当前所有快照与连接状态交给 AggregateHealth，返回一份独立报告。
// 传入聚合函数前先在锁内拷贝两份 map，既避免与并发写入竞争，也不在排序/分配
// 期间持有锁。返回报告与内部状态无别名，调用方可自由修改。
func (a *HealthAggregator) Report() MCPHealthReport {
	a.mu.RLock()
	reports := make(map[string]HealthSnapshot, len(a.reports))
	for name, snap := range a.reports {
		reports[name] = snap
	}
	states := make(map[string]ConnState, len(a.states))
	for name, state := range a.states {
		states[name] = state
	}
	a.mu.RUnlock()
	return AggregateHealth(reports, states)
}

// PollReporters 遍历已 Track 的 HealthReporter，调用其 HealthSnapshot() 把
// 最新快照写回聚合器（等价于对每个源执行一次 Observe）。它只做内存同步、
// 不发起任何 I/O，供调用方在轮次边界驱动。
//
// 调度前先在锁内取一份 reporter 快照并按名字排序，保证遍历顺序确定、且不在
// 持锁期间调用外部代码。nil reporter 被跳过。若某源在轮询途中被 Untrack，
// 其既已开始的刷新最多少一次写回，不会破坏聚合器的一致性。
func (a *HealthAggregator) PollReporters() {
	a.mu.RLock()
	type entry struct {
		name string
		rep  HealthReporter
	}
	entries := make([]entry, 0, len(a.reporters))
	for name, rep := range a.reporters {
		entries = append(entries, entry{name: name, rep: rep})
	}
	a.mu.RUnlock()

	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	for _, e := range entries {
		if e.rep == nil {
			continue
		}
		a.Observe(e.name, e.rep.HealthSnapshot())
	}
}

// initLocked 惰性分配三个 map，供零值聚合器使用。调用方须持有 a.mu。
func (a *HealthAggregator) initLocked() {
	if a.reporters == nil {
		a.reporters = make(map[string]HealthReporter)
	}
	if a.reports == nil {
		a.reports = make(map[string]HealthSnapshot)
	}
	if a.states == nil {
		a.states = make(map[string]ConnState)
	}
}

// healthDerivedState 按快照健康度派生连接状态。
func healthDerivedState(h HealthSnapshot) ConnState {
	if h.Healthy {
		return ConnReady
	}
	return ConnFailed
}
