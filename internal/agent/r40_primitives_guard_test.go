package agent

import (
	"math"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

// r40RunConcurrently 并行执行 fn 共 n 次并等待全部结束，
// 用于纯函数的并发只读守卫（fn 不得写入共享入参）。
func r40RunConcurrently(n int, fn func(i int)) {
	var wg sync.WaitGroup
	start := make(chan struct{})
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			<-start
			fn(i)
		}(i)
	}
	close(start)
	wg.Wait()
}

// TestR40GuardForecastDeterministicAndPure 锁定 ForecastNextTokens 的确定性、
// 入参不变性与并发只读安全；覆盖空/单样本、负样本与 int 极值。
func TestR40GuardForecastDeterministicAndPure(t *testing.T) {
	cases := [][]int{
		nil,
		{},
		{42},
		{-9},
		{100, 200, 300, 400},
		{400, 300, 200, 100},
		{-100, -50, -10, -1},
		{math.MinInt, 10, 20, 30},
		{math.MinInt, math.MaxInt},
		{math.MaxInt, math.MaxInt, math.MaxInt, math.MaxInt},
		{1000, 1000, 10, 20, 30, 40},
	}
	for idx, in := range cases {
		snapshot := slices.Clone(in)
		want := ForecastNextTokens(in)
		for rep := 0; rep < 64; rep++ {
			if got := ForecastNextTokens(in); got != want {
				t.Fatalf("case %d rep %d: ForecastNextTokens = %d, want %d", idx, rep, got, want)
			}
		}
		// 结果必须为合法 int（非负、不回绕），这是饱和算术的核心承诺。
		if want < 0 || want > math.MaxInt {
			t.Fatalf("case %d: 越界结果 %d", idx, want)
		}
		if !slices.Equal(in, snapshot) {
			t.Fatalf("case %d: 入参被修改: got %v, want %v", idx, in, snapshot)
		}
		r40RunConcurrently(16, func(_ int) {
			if got := ForecastNextTokens(in); got != want {
				t.Errorf("case %d: 并发只读结果 %d, want %d", idx, got, want)
			}
		})
	}
}

// TestR40GuardBudgetPrimitivesDeterministic 锁定 WillExceedBudget 与
// SuggestTrimTokens 的确定性、边界语义与并发只读安全。
func TestR40GuardBudgetPrimitivesDeterministic(t *testing.T) {
	cases := []struct{ forecast, used, budget int }{
		{39, 60, 100},
		{40, 60, 100},
		{41, 60, 100},
		{1000, 1000, 0},
		{1000, 1000, -1},
		{-5, -5, 100},
		{math.MaxInt, math.MaxInt, math.MaxInt},
		{math.MinInt, math.MinInt, math.MaxInt},
	}
	for idx, tc := range cases {
		wantExceed := WillExceedBudget(tc.forecast, tc.used, tc.budget)
		wantTrim := SuggestTrimTokens(tc.used, tc.forecast, tc.budget)
		for rep := 0; rep < 32; rep++ {
			if got := WillExceedBudget(tc.forecast, tc.used, tc.budget); got != wantExceed {
				t.Fatalf("case %d rep %d: WillExceedBudget = %v, want %v", idx, rep, got, wantExceed)
			}
			if got := SuggestTrimTokens(tc.used, tc.forecast, tc.budget); got != wantTrim {
				t.Fatalf("case %d rep %d: SuggestTrimTokens = %d, want %d", idx, rep, got, wantTrim)
			}
		}
		if wantTrim < 0 || wantTrim > math.MaxInt {
			t.Fatalf("case %d: 裁剪建议越界 %d", idx, wantTrim)
		}
		r40RunConcurrently(16, func(_ int) {
			if got := WillExceedBudget(tc.forecast, tc.used, tc.budget); got != wantExceed {
				t.Errorf("case %d: 并发 WillExceedBudget = %v, want %v", idx, got, wantExceed)
			}
			if got := SuggestTrimTokens(tc.used, tc.forecast, tc.budget); got != wantTrim {
				t.Errorf("case %d: 并发 SuggestTrimTokens = %d, want %d", idx, got, wantTrim)
			}
		})
	}
}

// TestR40GuardArithmeticHelpersDeterministic 对饱和算术辅助函数做极值与并发守卫。
// 这些辅助函数是 Forecast/Budget 系列原语不溢出的基础，必须逐值确定。
func TestR40GuardArithmeticHelpersDeterministic(t *testing.T) {
	pairs := [][2]int64{
		{0, 0},
		{10, -3},
		{math.MaxInt64 - 1, 1},
		{math.MaxInt64, 1},
		{math.MinInt64, -1},
		{math.MinInt64 + 1, -1},
	}
	for _, p := range pairs {
		want := satAdd(p[0], p[1])
		for rep := 0; rep < 16; rep++ {
			if got := satAdd(p[0], p[1]); got != want {
				t.Fatalf("satAdd(%d,%d) rep %d = %d, want %d", p[0], p[1], rep, got, want)
			}
		}
		r40RunConcurrently(8, func(_ int) {
			if got := satAdd(p[0], p[1]); got != want {
				t.Errorf("并发 satAdd(%d,%d) = %d, want %d", p[0], p[1], got, want)
			}
		})
	}
	divs := []struct{ numer, denom int64 }{
		{0, 1}, {1, 2}, {-1, 2}, {5, 2}, {-5, 2},
		{math.MaxInt64, 3}, {math.MinInt64, 3}, {7, 0}, {7, -2},
	}
	for _, d := range divs {
		want := divRound(d.numer, d.denom)
		for rep := 0; rep < 16; rep++ {
			if got := divRound(d.numer, d.denom); got != want {
				t.Fatalf("divRound(%d,%d) rep %d = %d, want %d", d.numer, d.denom, rep, got, want)
			}
		}
	}
	for _, v := range []int64{math.MinInt64, -1, 0, 1, math.MaxInt64} {
		wantNonNeg, wantClamp := nonNeg(v), clampToInt(v)
		for rep := 0; rep < 16; rep++ {
			if nonNeg(v) != wantNonNeg || clampToInt(v) != wantClamp {
				t.Fatalf("nonNeg/clampToInt(%d) 结果不稳定", v)
			}
		}
	}
}

// TestR40GuardStatsFromSamplesDeterministicAndPure 锁定 statsFromSamples 的
// 逐字段确定性、只读入参与并发只读安全；含空、单样本、重复值与零值样本。
func TestR40GuardStatsFromSamplesDeterministicAndPure(t *testing.T) {
	ms := time.Millisecond
	cases := [][]time.Duration{
		nil,
		{},
		{5 * ms},
		{10 * ms, 20 * ms, 30 * ms, 40 * ms, 100 * ms},
		{30 * ms, 10 * ms, 20 * ms},
		{0, 0, 0},
		{7 * ms, 7 * ms, 7 * ms, 7 * ms},
	}
	for idx, in := range cases {
		snapshot := slices.Clone(in)
		want := statsFromSamples(in)
		for rep := 0; rep < 64; rep++ {
			got := statsFromSamples(in)
			// 逐字段一致，而非只比较整体相等。
			if got.Count != want.Count || got.Total != want.Total ||
				got.Min != want.Min || got.Max != want.Max ||
				got.Mean != want.Mean || got.P50 != want.P50 || got.P95 != want.P95 {
				t.Fatalf("case %d rep %d: stats = %+v, want %+v", idx, rep, got, want)
			}
		}
		if !slices.Equal(in, snapshot) {
			t.Fatalf("case %d: statsFromSamples 修改了入参: got %v, want %v", idx, in, snapshot)
		}
		r40RunConcurrently(16, func(_ int) {
			if got := statsFromSamples(in); got != want {
				t.Errorf("case %d: 并发 statsFromSamples = %+v, want %+v", idx, got, want)
			}
		})
	}
}

// TestR40GuardPercentileDeterministic 覆盖不同样本量与分位数的整数最近秩算法。
func TestR40GuardPercentileDeterministic(t *testing.T) {
	ms := time.Millisecond
	cases := []struct {
		sorted   []time.Duration
		permille int
	}{
		{nil, 500},
		{[]time.Duration{5 * ms}, 500},
		{[]time.Duration{10 * ms, 20 * ms}, 950},
		{[]time.Duration{10 * ms, 20 * ms, 30 * ms}, 500},
		{[]time.Duration{10 * ms, 20 * ms, 30 * ms, 40 * ms, 100 * ms}, 950},
	}
	for idx, tc := range cases {
		snapshot := slices.Clone(tc.sorted)
		want := percentileOf(tc.sorted, tc.permille)
		for rep := 0; rep < 32; rep++ {
			if got := percentileOf(tc.sorted, tc.permille); got != want {
				t.Fatalf("case %d rep %d: percentile = %v, want %v", idx, rep, got, want)
			}
		}
		if !slices.Equal(tc.sorted, snapshot) {
			t.Fatalf("case %d: percentileOf 修改了入参", idx)
		}
	}
}

// TestR40GuardHistogramCJKAndConcurrent 验证带锁的 ToolTimingHistogram 在
// CJK/emoji 工具名与读写混合并发下无竞态、统计自洽且 TopSlow 顺序确定、返回值为副本。
func TestR40GuardHistogramCJKAndConcurrent(t *testing.T) {
	names := []string{"读取文件", "写入文件", "shell", "emoji🚀", "café", "回滚事务"}
	for _, name := range names {
		if !utf8.ValidString(name) {
			t.Fatalf("测试输入不是合法 UTF-8: %q", name)
		}
	}

	var h ToolTimingHistogram
	r40RunConcurrently(48, func(i int) {
		name := names[i%len(names)]
		h.Record(name, time.Duration(i%9+1)*time.Millisecond)
		_ = h.Stats(name)
		_ = h.TopSlow(3)
	})

	for _, name := range names {
		s := h.Stats(name)
		if s.Count == 0 || s.Total <= 0 || s.Min < 0 || s.Max < s.Min || s.P95 < s.P50 {
			t.Errorf("工具 %q 统计不自洽: %+v", name, s)
		}
	}

	// 同一状态反复调用 TopSlow，顺序与每个字段都必须一致。
	want := h.TopSlow(len(names))
	for rep := 0; rep < 50; rep++ {
		got := h.TopSlow(len(names))
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("rep %d: TopSlow 不稳定:\n got %+v\nwant %+v", rep, got, want)
		}
	}

	// 修改返回值不得影响内部状态。
	got := h.TopSlow(len(names))
	got[0].Tool = "mutated"
	got[0].Stats.Count = -1
	for _, item := range h.TopSlow(len(names)) {
		if item.Tool == "mutated" || item.Stats.Count == -1 {
			t.Fatalf("TopSlow 返回值不是副本: %+v", item)
		}
	}
}

// TestR40GuardHistogramTieOrderStable 在 P95 与 Total 完全并列时，
// 反复调用 TopSlow 必须按工具名字典序稳定输出（含 CJK 名称的字节序）。
func TestR40GuardHistogramTieOrderStable(t *testing.T) {
	var h ToolTimingHistogram
	for _, name := range []string{"zeta", "读取", "alpha", "写入", "beta"} {
		h.Record(name, 10*time.Millisecond)
		h.Record(name, 20*time.Millisecond)
	}
	want := h.TopSlow(5)
	for rep := 0; rep < 100; rep++ {
		got := h.TopSlow(5)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("rep %d: 并列顺序不稳定:\n got %+v\nwant %+v", rep, got, want)
		}
	}
}
