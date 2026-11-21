package agent

import (
	"sort"
	"sync"
	"time"
)

// 本文件提供 ToolTimingHistogram：按工具名记录单次调用的耗时样本，并给出
// count/total/min/max/mean/p50/p95 等分布统计，用于回答“哪个工具最慢、p95 是多少”。
// 与 turn_timing 一样，它是零依赖纯原语：不读写时钟以外的全局量、不做 I/O、不接入
// 实际工具执行。接线层在需要时再决定何时 Record 以及采样率、落库方式。

// ToolTimingStats 是单个工具的耗时分布快照。所有字段都是值类型（无切片/指针），
// 调用方持有后不会被内部后续写入改变。
type ToolTimingStats struct {
	Count int           // 有效样本数（钳制后的样本仍计入）
	Total time.Duration // 所有样本之和
	Min   time.Duration // 最小样本（钳制后 >= 0）
	Max   time.Duration // 最大样本
	Mean  time.Duration // Total / Count，整除取整
	P50   time.Duration // 第 50 分位（最近秩，近似）
	P95   time.Duration // 第 95 分位（最近秩，近似）
}

// ToolTimingSlow 是 TopSlow 返回的一项：工具名与该工具耗时统计的独立副本。
type ToolTimingSlow struct {
	Tool  string
	Stats ToolTimingStats
}

// ToolTimingHistogram 按工具名累积耗时样本。
//
// 零值即可使用：samples 为 nil 时 Record 会惰性 make，读方法对 nil map 安全。
// 所有方法使用指针接收者并共用 mu，可被多 goroutine 并发调用；mu 是值字段而非
// 指针，因为本类型从不按值复制，不存在 copylocks 风险。
type ToolTimingHistogram struct {
	mu      sync.Mutex
	samples map[string][]time.Duration
}

// Record 记录一次工具调用的耗时 d。负数或零一律钳制为 0 后再入样，避免异常值
// 拉低 min/mean；钳制后的样本仍计入 Count，使统计口径保持统一。
func (h *ToolTimingHistogram) Record(tool string, d time.Duration) {
	if d <= 0 {
		d = 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.samples == nil {
		h.samples = make(map[string][]time.Duration)
	}
	h.samples[tool] = append(h.samples[tool], d)
}

// Stats 返回工具 tool 的耗时分布快照；未记录过的工具返回零值统计（Count=0）。
// 计算在锁内完成且只复制样本后排序，不原地改动内部切片，也不保留内部引用。
func (h *ToolTimingHistogram) Stats(tool string) ToolTimingStats {
	h.mu.Lock()
	defer h.mu.Unlock()
	return statsFromSamples(h.samples[tool])
}

// TopSlow 返回最慢的前 n 个工具：按 P95 降序，P95 相同按 Total 降序，仍相同
// 按工具名字典序升序（保证确定性）。n<=0 返回空切片。
// 每项携带统计副本，修改返回值不会影响内部状态。
func (h *ToolTimingHistogram) TopSlow(n int) []ToolTimingSlow {
	if n <= 0 {
		return []ToolTimingSlow{}
	}
	h.mu.Lock()
	entries := make([]ToolTimingSlow, 0, len(h.samples))
	for tool, samples := range h.samples {
		entries = append(entries, ToolTimingSlow{Tool: tool, Stats: statsFromSamples(samples)})
	}
	h.mu.Unlock()

	sort.Slice(entries, func(i, j int) bool {
		a, b := entries[i].Stats, entries[j].Stats
		if a.P95 != b.P95 {
			return a.P95 > b.P95
		}
		if a.Total != b.Total {
			return a.Total > b.Total
		}
		return entries[i].Tool < entries[j].Tool
	})
	if len(entries) > n {
		entries = entries[:n]
	}
	return entries
}

// statsFromSamples 由样本切片计算统计。samples 只读：内部先复制到新切片再排序，
// 绝不修改入参，也不保留对入参的引用。空样本返回全零统计。
func statsFromSamples(samples []time.Duration) ToolTimingStats {
	var s ToolTimingStats
	n := len(samples)
	if n == 0 {
		return s
	}
	s.Count = n

	sorted := make([]time.Duration, n)
	copy(sorted, samples)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	s.Min = sorted[0]
	s.Max = sorted[n-1]
	for _, d := range sorted {
		s.Total += d
	}
	s.Mean = s.Total / time.Duration(n)
	s.P50 = percentileOf(sorted, 500)
	s.P95 = percentileOf(sorted, 950)
	return s
}

// percentileOf 用最近秩（nearest-rank）法取第 permille/1000 分位数：
// rank = ceil(permille*n/1000)，索引 = rank-1，并夹到 [0, n-1]。
// 纯整数运算，无浮点误差，同一组样本结果确定。
func percentileOf(sorted []time.Duration, permille int) time.Duration {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	rank := (permille*n + 999) / 1000 // 整数形式的 ceil
	if rank < 1 {
		rank = 1
	}
	if rank > n {
		rank = n
	}
	return sorted[rank-1]
}
