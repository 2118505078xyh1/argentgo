package agent

import "sync"

// 本文件提供一个只读诊断原语：按失败种类累计"恢复尝试是否真的成功"的指标。
// 与现有的 mistake/stuck/no_progress 只记录失败发生次数不同，这里回答的是
// 另一个问题——恢复动作之后运行是否真的恢复了。它纯内存、无 I/O、不读全局
// 状态、不感知提示或地区。已由 loop.go 的恢复成效接线（默认开启、
// ARGENT_RECOVERY_METRICS=0 关闭）在指引注入与工具成功处采集；=0 关闭时不分配、零开销。

// RecoveryOutcome 是某一种失败最近一次恢复动作的结果。
//
// 三种取值对应一次恢复循环的三种收尾方式：尝试后仍未摆脱故障（persisted）、
// 尝试后确实恢复（recovered）、或放弃本轮并升级处理（escalated）。
type RecoveryOutcome string

const (
	// RecoveryOutcomeRecovered 表示最近一次恢复动作之后运行确实成功了。
	RecoveryOutcomeRecovered RecoveryOutcome = "recovered"
	// RecoveryOutcomePersisted 表示只记录了一次恢复尝试，故障尚未被证明已消除。
	RecoveryOutcomePersisted RecoveryOutcome = "persisted"
	// RecoveryOutcomeEscalated 表示恢复被放弃，故障升级交由上层处理。
	RecoveryOutcomeEscalated RecoveryOutcome = "escalated"
)

// RecoveryStats 是单一失败种类累计到的恢复指标。
//
// Attempts 为恢复尝试次数；Successes 为其中确实成功的次数。Rate 是
// Successes/Attempts 的成功率，恒被钳制在 [0,1]；Attempts 为零时 Rate 为零
// （避免 0/0 产生 NaN）。Outcome 是该种类最近一次恢复动作的结果。
type RecoveryStats struct {
	Attempts  int
	Successes int
	Rate      float64
	Outcome   RecoveryOutcome
}

// RecoveryMetrics 按失败种类累计恢复指标。所有方法都可安全并发调用。
//
// 零值即可用：byKind 延迟初始化，因此 var m RecoveryMetrics 与
// &RecoveryMetrics{} 行为一致，默认空（无任何种类记录）。nil 接收者同样
// 安全——所有方法直接返回零值/空快照，便于调用方省略判空。
type RecoveryMetrics struct {
	mu     sync.RWMutex
	byKind map[string]recoveryCounts
}

// recoveryCounts 是内部累计值。Rate 不在这里保存，而是在 Snapshot 时由
// 整数计数现算，因此成功率是计数的确定性函数，不会因增量浮点更新而漂移。
type recoveryCounts struct {
	attempts  int
	successes int
	outcome   RecoveryOutcome
}

// RecordAttempt 记录针对 kind 的一次恢复尝试，并把最近结果标记为 persisted
// （尝试已开始，但尚未证明成功）。kind 可以是任意字符串，包括空串。
func (m *RecoveryMetrics) RecordAttempt(kind string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.init(kind)
	c.attempts++
	c.outcome = RecoveryOutcomePersisted
	m.byKind[kind] = c
}

// RecordSuccess 记录针对 kind 的一次成功恢复，并把最近结果标记为 recovered。
// 它不递增 Attempts：调用方在恢复成功时同时记录一次尝试与一次成功，
// 使 Rate 恒为"成功数/尝试数"。若只记录成功而未记录尝试，该种类在
// Snapshot 中的 Attempts 为零、Rate 为零。
func (m *RecoveryMetrics) RecordSuccess(kind string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.init(kind)
	c.successes++
	c.outcome = RecoveryOutcomeRecovered
	m.byKind[kind] = c
}

// RecordEscalation 记录针对 kind 的一次升级处理，并把最近结果标记为 escalated。
// 升级是一次恢复循环的收尾而非新的尝试，因此不递增 Attempts 或 Successes。
// 该方法让 RecoveryOutcome 的第三个取值可被表达；当前恢复路径尚未接线，
// 故本原语内没有其它方法会产出 escalated。
func (m *RecoveryMetrics) RecordEscalation(kind string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.init(kind)
	c.outcome = RecoveryOutcomeEscalated
	m.byKind[kind] = c
}

// init 保证内部 map 已初始化，并返回 kind 的当前计数。调用方必须持有写锁。
func (m *RecoveryMetrics) init(kind string) recoveryCounts {
	if m.byKind == nil {
		m.byKind = make(map[string]recoveryCounts)
	}
	return m.byKind[kind]
}

// Snapshot 返回当前所有种类指标的副本。返回值是新建的 map，调用方可随意
// 修改，不会影响 RecoveryMetrics 内部状态；RecoveryStats 全由值类型字段
// 组成，按值复制即完整副本。零值或 nil 的接收者返回非 nil 的空 map。
func (m *RecoveryMetrics) Snapshot() map[string]RecoveryStats {
	out := make(map[string]RecoveryStats)
	if m == nil {
		return out
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for kind, c := range m.byKind {
		out[kind] = RecoveryStats{
			Attempts:  c.attempts,
			Successes: c.successes,
			Rate:      recoveryRate(c.attempts, c.successes),
			Outcome:   c.outcome,
		}
	}
	return out
}

// recoveryRate 计算成功率并钳制到 [0,1]。零尝试返回零，避免 0/0 的 NaN；
// 成功数多于尝试数（调用方只记录成功）时钳制到 1。
func recoveryRate(attempts, successes int) float64 {
	if attempts <= 0 {
		return 0
	}
	rate := float64(successes) / float64(attempts)
	if rate < 0 {
		return 0
	}
	if rate > 1 {
		return 1
	}
	return rate
}
