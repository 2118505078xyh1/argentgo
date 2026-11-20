package agent

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

func TestCostWatcherThresholdsFireOnceEach(t *testing.T) {
	w := NewCostWatcher(0) // no budget nudge: just the five milestones
	got := w.Observe(0.12)
	if len(got) != 1 {
		t.Fatalf("first observe = %v, want one warning", got)
	}
	if !strings.Contains(got[0], "0.1000") || !strings.Contains(got[0], "0.1200") {
		t.Fatalf("warning = %q, want both the threshold and the current cost", got[0])
	}
	if got := w.Observe(0.12); len(got) != 0 {
		t.Fatalf("repeat observe = %v, want no warnings", got)
	}
	if got := w.Observe(0.6); len(got) != 1 || !strings.Contains(got[0], "0.5000") {
		t.Fatalf("second threshold = %v, want the $0.50 warning", got)
	}
	if got := w.Observe(10); len(got) != 3 {
		t.Fatalf("final observe = %v, want three warnings ($1, $5, $10)", got)
	}
	if got := w.Observe(20); len(got) != 0 {
		t.Fatalf("after all thresholds = %v, want no warnings", got)
	}
}

func TestCostWatcherIsMonotonic(t *testing.T) {
	w := NewCostWatcher(0)
	if got := w.Observe(1); len(got) != 3 {
		t.Fatalf("observe(1) = %v, want three warnings", got)
	}
	if got := w.Observe(0); len(got) != 0 {
		t.Fatalf("observe(0) after a dip = %v, want no warnings", got)
	}
	if got := w.Observe(0.9); len(got) != 0 {
		t.Fatalf("observe(0.9) after a dip = %v, want no warnings", got)
	}
}

func TestCostWatcherBudgets80PercentNudge(t *testing.T) {
	w := NewCostWatcher(1) // thresholds: 0.1, 0.5, 0.8, 1
	if got := w.Observe(0.7); len(got) != 2 {
		t.Fatalf("observe(0.7) = %v, want $0.10 and $0.50", got)
	}
	got := w.Observe(0.8)
	if len(got) != 1 || !strings.Contains(got[0], "0.8000") {
		t.Fatalf("nudge = %v, want one warning at $0.80", got)
	}
	if got := w.Observe(1); len(got) != 1 || !strings.Contains(got[0], "1.0000") {
		t.Fatalf("budget threshold = %v, want one warning at $1.00", got)
	}
}

func TestCostWatcherNudgeCoincidingWithMilestoneWarnsOnce(t *testing.T) {
	// 80% of 0.625 is exactly 0.50, which is already a fixed milestone.
	w := NewCostWatcher(0.625)
	if got := w.Observe(0.5); len(got) != 2 {
		t.Fatalf("observe(0.5) = %v, want only $0.10 and $0.50", got)
	}
}

func TestCostWatcherNoWarningBelowThreshold(t *testing.T) {
	w := NewCostWatcher(1)
	for _, cost := range []float64{0, 0.01, 0.099} {
		if got := w.Observe(cost); len(got) != 0 {
			t.Fatalf("observe(%v) = %v, want no warnings", cost, got)
		}
	}
	if got := w.Observe(0.1); len(got) != 1 {
		t.Fatalf("observe(0.1) = %v, want one warning", got)
	}
}

func TestCostWatcherNonPositiveBudgetUsesDefaultsOnly(t *testing.T) {
	for _, budget := range []float64{0, -1, -100} {
		w := NewCostWatcher(budget)
		if got := w.Observe(10); len(got) != len(defaultCostThresholds) {
			t.Fatalf("budget %v: got %d warnings, want %d", budget, len(got), len(defaultCostThresholds))
		}
		if got := w.Observe(100); len(got) != 0 {
			t.Fatalf("budget %v: repeated warnings %v", budget, got)
		}
	}
}

func TestCostWatcherReset(t *testing.T) {
	w := NewCostWatcher(0)
	if got := w.Observe(1); len(got) != 3 {
		t.Fatalf("initial = %v, want three warnings", got)
	}
	if got := w.Observe(1); len(got) != 0 {
		t.Fatalf("before reset = %v, want none", got)
	}
	w.Reset()
	if got := w.Observe(1); len(got) != 3 {
		t.Fatalf("after reset = %v, want three warnings again", got)
	}
}

func TestCostWatcherConcurrentObserve(t *testing.T) {
	w := NewCostWatcher(10) // five milestones plus the 80%-of-budget nudge
	wantTotal := len(defaultCostThresholds) + 1

	const goroutines = 24
	var wg sync.WaitGroup
	var total atomic.Int64
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for _, warning := range w.Observe(float64(n) * 0.5) {
				if !utf8.ValidString(warning) || !strings.HasPrefix(warning, "cost warning: ") {
					t.Errorf("bad warning %q", warning)
				}
				total.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if got := int(total.Load()); got != wantTotal {
		t.Fatalf("total warnings = %d, want %d (one per threshold)", got, wantTotal)
	}
	if got := len(w.Observe(100)); got != 0 {
		t.Fatalf("observe after concurrent calls = %v, want none", got)
	}
}

func TestCostWarningsAreEmittedAsNotices(t *testing.T) {
	chat := &fakeChatter{fn: func(int, []llm.Message, []llm.ToolSpec, func(string)) (llm.Message, error) {
		reply := textReply("done")
		reply.Usage = &llm.Usage{PromptTokens: 1000}
		return reply, nil
	}}
	var notices []string
	runner := &Runner{Opts: Options{
		Model:   "m",
		Chatter: chat,
		Tools:   &fakeTools{},
		Budget:  Budget{MaxCostUSD: 5},
		Prices:  map[string]Price{"m": {Input: 100}}, // 1000 tokens = $0.10
		Emit: func(e Event) {
			if e.Kind == EventNotice && strings.HasPrefix(e.Text, "cost warning: ") {
				notices = append(notices, e.Text)
			}
		},
	}}
	res, err := runner.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Stopped != StopComplete {
		t.Fatalf("stopped = %q, want %q", res.Stopped, StopComplete)
	}
	if len(notices) != 1 {
		t.Fatalf("cost notices = %v, want one ($0.10)", notices)
	}
	if !strings.Contains(notices[0], "0.1000") {
		t.Fatalf("notice = %q, want the $0.10 threshold", notices[0])
	}
}
