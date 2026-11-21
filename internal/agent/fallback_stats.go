package agent

import "sync"

// 本文件提供一个只读诊断原语：按模型回退的切换原因累计"回退之后是否真的成功"的指标。
// fallback.go 中每次尝试的成败是即时的，EventRetry 只报告切换原因，二者都不保留跨运行的
// 成功率。这里填补这一空白：回答"因为某个原因切到备用模型后，成功占比是多少"。
// 它刻意不接线：纯内存、无 I/O、不读全局状态、不感知提示或地区。当前只作为可并发调用的
// 计数器存在，未来可由调用方把这些指标汇聚到状态端点或审计日志。

// FallbackReason 是触发一次模型回退的切换原因标记（例如限流、超时、不可达）。
// 它是字符串类型，可承载任意标记，包括 CJK 文本；原语只做透传，不解析、不改写。
type FallbackReason string

// fallbackCounts 是某一原因的内部累计值。Rate 不在这里保存，而是在 Snapshot 时由
// 整数计数现算，因此成功率是计数的确定性函数，不会因增量浮点更新而漂移。
type fallbackCounts struct {
	attempts  int
	successes int
}

// FallbackStats 按切换原因累计模型回退的成功率。所有方法都可安全并发调用。
//
// 零值即可用：byReason 延迟初始化，因此 var s FallbackStats 与
// &FallbackStats{} 行为一致，默认空（无任何原因记录）。nil 接收者同样
// 安全——所有方法直接返回零值/空快照，便于调用方省略判空。
type FallbackStats struct {
	mu       sync.RWMutex
	byReason map[FallbackReason]fallbackCounts
}

// Record 记录针对 reason 的一次回退尝试；succeeded 表示该次尝试最终是否成功。
// 每次调用都递增 Attempts，succeeded 为真时额外递增 Successes，使 Rate 恒为
// "成功数/尝试数"。reason 可以是任意值，包括空串；原语按值接收，不修改入参。
func (s *FallbackStats) Record(reason FallbackReason, succeeded bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byReason == nil {
		s.byReason = make(map[FallbackReason]fallbackCounts)
	}
	c := s.byReason[reason]
	c.attempts++
	if succeeded {
		c.successes++
	}
	s.byReason[reason] = c
}

// Snapshot 返回当前所有原因指标的副本。返回值是新建的 map，调用方可随意修改，
// 不会影响 FallbackStats 内部状态；结果结构全由值类型字段组成，按值复制即完整副本。
// 零值或 nil 的接收者返回非 nil 的空 map。
func (s *FallbackStats) Snapshot() map[FallbackReason]struct {
	Attempts  int
	Successes int
	Rate      float64
} {
	out := make(map[FallbackReason]struct {
		Attempts  int
		Successes int
		Rate      float64
	})
	if s == nil {
		return out
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for reason, c := range s.byReason {
		out[reason] = struct {
			Attempts  int
			Successes int
			Rate      float64
		}{
			Attempts:  c.attempts,
			Successes: c.successes,
			Rate:      fallbackRate(c.attempts, c.successes),
		}
	}
	return out
}

// fallbackRate 计算成功率并钳制到 [0,1]。零尝试返回零，避免 0/0 的 NaN；
// 成功数多于尝试数（调用方误记）时钳制到 1，负数被钳制到 0。
func fallbackRate(attempts, successes int) float64 {
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
