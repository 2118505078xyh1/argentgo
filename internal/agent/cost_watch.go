package agent

import (
	"fmt"
	"sort"
	"sync"
)

// defaultCostThresholds 是固定的花费里程碑（美元），达到时一次运行会给出一次性警告。
// 它们借鉴了 gptme 的成本感知钩子：随着花费上升就提醒操作人员，
// 而不只是在硬性预算使运行停止时才提醒。
var defaultCostThresholds = []float64{0.10, 0.50, 1, 5, 10}

// CostWatcher 在累计花费每次越过配置的阈值时报告一条警告。它只保存状态：
// 无 I/O、无时钟、无随机性。Observe 可以被多个 goroutine 安全地调用。
type CostWatcher struct {
	mu         sync.Mutex
	thresholds []float64
	crossed    map[int]bool // 按 thresholds 中的位置索引
	lastCost   float64
}

// NewCostWatcher 为固定里程碑构建一个观察器，并在 budgetUSD 为正时额外加入
// 预算 80% 的提醒。阈值会排序并去重，因此恰好落在某个里程碑上的提醒只会告警一次。
func NewCostWatcher(budgetUSD float64) *CostWatcher {
	thresholds := append([]float64(nil), defaultCostThresholds...)
	if budgetUSD > 0 {
		thresholds = append(thresholds, budgetUSD*0.8)
	}
	sort.Float64s(thresholds)
	unique := thresholds[:0]
	for _, threshold := range thresholds {
		if len(unique) == 0 || threshold != unique[len(unique)-1] {
			unique = append(unique, threshold)
		}
	}
	return &CostWatcher{
		thresholds: unique,
		crossed:    make(map[int]bool, len(unique)),
	}
}

// Observe 记录当前的累计花费，并按升序为自上次调用以来新越过的每个阈值返回一条警告。
// 在 Reset 之前，每个阈值最多触发一次；花费回落不会让已越过的阈值"撤销"。
// nil 接收者是安全的，因此未经 newRunState 构造的 runState 也能调用它。
func (w *CostWatcher) Observe(costUSD float64) []string {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	if costUSD < w.lastCost {
		costUSD = w.lastCost
	}
	w.lastCost = costUSD

	var warnings []string
	for i, threshold := range w.thresholds {
		if w.crossed[i] || costUSD < threshold {
			continue
		}
		w.crossed[i] = true
		warnings = append(warnings, fmt.Sprintf(
			"cost warning: spend $%.4f crossed the $%.4f threshold", costUSD, threshold,
		))
	}
	return warnings
}

// Reset 清除已越过的阈值，使该观察器能被预算相同的新运行复用。
func (w *CostWatcher) Reset() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for i := range w.crossed {
		w.crossed[i] = false
	}
	w.lastCost = 0
}
