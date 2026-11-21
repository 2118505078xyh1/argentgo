package llm

import (
	"math"
	"testing"
)

func TestBackoffDelayExponentialGrowth(t *testing.T) {
	cfg := BackoffConfig{BaseMs: 100, MaxMs: 100000, Factor: 2}
	want := []int64{100, 200, 400, 800, 1600, 3200}
	for attempt, w := range want {
		if got := BackoffDelay(attempt, cfg, 0); got != w {
			t.Fatalf("BackoffDelay(%d) = %d, want %d", attempt, got, w)
		}
	}
}

func TestBackoffDelayMaxCap(t *testing.T) {
	cfg := BackoffConfig{BaseMs: 100, MaxMs: 1000, Factor: 2}
	for _, attempt := range []int{4, 5, 10, 100} {
		if got := BackoffDelay(attempt, cfg, 0); got != 1000 {
			t.Fatalf("BackoffDelay(%d) = %d, want capped 1000", attempt, got)
		}
	}
	// 未触及上限时保持精确增长。
	if got := BackoffDelay(3, cfg, 0); got != 800 {
		t.Fatalf("BackoffDelay(3) = %d, want 800", got)
	}
}

func TestBackoffDelayJitterDeterministic(t *testing.T) {
	cfg := BackoffConfig{BaseMs: 1000, MaxMs: 100000, Factor: 2, Jitter: true}
	for _, attempt := range []int{0, 1, 3, 7} {
		first := BackoffDelay(attempt, cfg, 42)
		for i := 0; i < 50; i++ {
			if got := BackoffDelay(attempt, cfg, 42); got != first {
				t.Fatalf("attempt=%d seed=42: got %d, want deterministic %d", attempt, got, first)
			}
		}
	}
	// 不同 seed 应产生不同分布；若全部相同则 hash 没有起到分散作用。
	seen := make(map[int64]bool)
	for seed := int64(0); seed < 32; seed++ {
		seen[BackoffDelay(2, cfg, seed)] = true
	}
	if len(seen) < 2 {
		t.Fatal("jitter must vary with seed")
	}
}

func TestBackoffDelayJitterInRange(t *testing.T) {
	cfg := BackoffConfig{BaseMs: 1000, MaxMs: 100000, Factor: 2, Jitter: true}
	// attempt=0 的计算值是 BaseMs=1000；抖动应落在 [500, 1000]。
	for seed := int64(-8); seed <= 8; seed++ {
		got := BackoffDelay(0, cfg, seed)
		if got < 500 || got > 1000 {
			t.Fatalf("seed=%d: jitter delay %d out of [500,1000]", seed, got)
		}
	}
	// 关闭 jitter 时恰好是计算值。
	cfg.Jitter = false
	if got := BackoffDelay(0, cfg, 123); got != 1000 {
		t.Fatalf("no jitter = %d, want 1000", got)
	}
}

func TestBackoffDelayAttemptBoundary(t *testing.T) {
	cfg := BackoffConfig{BaseMs: 500, MaxMs: 100000, Factor: 3}
	for _, attempt := range []int{-100, -1, 0} {
		if got := BackoffDelay(attempt, cfg, 0); got != 500 {
			t.Fatalf("attempt=%d = %d, want base 500 (clamped to 0)", attempt, got)
		}
	}
	if got := BackoffDelay(1, cfg, 0); got != 1500 {
		t.Fatalf("attempt=1 = %d, want 1500", got)
	}
}

func TestBackoffDelayFactorBoundary(t *testing.T) {
	// 非正数、NaN、无穷大按 1 处理：不增长、不产生 NaN、不溢出。
	bad := []float64{0, -1, -2.5, math.NaN(), math.Inf(1), math.Inf(-1)}
	for _, f := range bad {
		cfg := BackoffConfig{BaseMs: 700, MaxMs: 100000, Factor: f}
		if got := BackoffDelay(5, cfg, 0); got != 700 {
			t.Fatalf("Factor=%v = %d, want base 700", f, got)
		}
	}
	// Factor=1 恒等；Factor<1 时延迟缩短但仍非负。
	unit := BackoffConfig{BaseMs: 800, MaxMs: 0, Factor: 1}
	if got := BackoffDelay(9, unit, 0); got != 800 {
		t.Fatalf("Factor=1 = %d, want 800", got)
	}
	cfg := BackoffConfig{BaseMs: 800, MaxMs: 0, Factor: 0.5}
	if got := BackoffDelay(3, cfg, 0); got != 100 {
		t.Fatalf("Factor=0.5 attempt=3 = %d, want 100", got)
	}
}

func TestBackoffDelayOverflowGuard(t *testing.T) {
	// 大基数 × 大指数 × 大 attempt：不得回绕为负，必须钳制或饱和。
	cases := []struct {
		name string
		cfg  BackoffConfig
		want int64
	}{
		{"capped", BackoffConfig{BaseMs: maxBackoffMs, MaxMs: 5000, Factor: 2}, 5000},
		{"uncapped", BackoffConfig{BaseMs: maxBackoffMs, MaxMs: 0, Factor: 2}, maxBackoffMs},
		{"huge-attempt", BackoffConfig{BaseMs: 1000, MaxMs: 0, Factor: 2}, maxBackoffMs},
		{"zero-base", BackoffConfig{BaseMs: 0, MaxMs: 1000, Factor: 2}, 0},
		{"negative-base", BackoffConfig{BaseMs: -100, MaxMs: 1000, Factor: 2}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := BackoffDelay(1<<30, tc.cfg, 7)
			if got != tc.want {
				t.Fatalf("BackoffDelay = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestBackoffDelayDoesNotMutateConfig(t *testing.T) {
	cfg := BackoffConfig{BaseMs: 300, MaxMs: 5000, Factor: 2.5, Jitter: true}
	before := cfg
	for _, attempt := range []int{-1, 0, 4, 1000} {
		BackoffDelay(attempt, cfg, 99)
	}
	if cfg != before {
		t.Fatalf("config mutated: %+v, want %+v", cfg, before)
	}
}
