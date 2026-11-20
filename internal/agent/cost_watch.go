package agent

import (
	"fmt"
	"sort"
	"sync"
)

// defaultCostThresholds are the fixed spend milestones (USD) at which a run
// gives a one-time warning. They follow gptme's cost-awareness hook: the
// operator is nudged as spend climbs, not only when a hard budget stops the run.
var defaultCostThresholds = []float64{0.10, 0.50, 1, 5, 10}

// CostWatcher reports one warning each time accumulated cost crosses a
// configured threshold. It keeps only state: no I/O, no clock, no randomness.
// Observe is safe to call from several goroutines.
type CostWatcher struct {
	mu         sync.Mutex
	thresholds []float64
	crossed    map[int]bool // indexed by position in thresholds
	lastCost   float64
}

// NewCostWatcher builds a watcher for the fixed milestones plus an
// 80%-of-budget nudge when budgetUSD is positive. Thresholds are sorted and
// de-duplicated, so a nudge that lands on a milestone warns only once.
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

// Observe records the current accumulated cost and returns a warning for each
// threshold newly crossed since the previous call, in ascending order. A
// threshold fires at most once until Reset; a dip in cost cannot un-cross one.
// The nil receiver is safe so a runState built without newRunState can call it.
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

// Reset clears the crossed thresholds so the watcher can be reused by a new
// run with the same budget.
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
