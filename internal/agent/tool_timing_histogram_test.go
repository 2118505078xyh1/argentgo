package agent

import (
	"sync"
	"testing"
	"time"
)

// TestToolTimingHistogramStats 验证单工具各统计字段的计算：总数、总和、极值、均值与分位数。
func TestToolTimingHistogramStats(t *testing.T) {
	var h ToolTimingHistogram
	for _, d := range []time.Duration{
		10 * time.Millisecond,
		20 * time.Millisecond,
		30 * time.Millisecond,
		40 * time.Millisecond,
		100 * time.Millisecond,
	} {
		h.Record("shell", d)
	}

	got := h.Stats("shell")
	if got.Count != 5 {
		t.Fatalf("Count = %d, want 5", got.Count)
	}
	if got.Total != 200*time.Millisecond {
		t.Fatalf("Total = %v, want 200ms", got.Total)
	}
	if got.Min != 10*time.Millisecond {
		t.Fatalf("Min = %v, want 10ms", got.Min)
	}
	if got.Max != 100*time.Millisecond {
		t.Fatalf("Max = %v, want 100ms", got.Max)
	}
	if got.Mean != 40*time.Millisecond {
		t.Fatalf("Mean = %v, want 40ms", got.Mean)
	}
	// 最近秩：P50 取 sorted[2]=30ms，P95 取 sorted[4]=100ms。
	if got.P50 != 30*time.Millisecond {
		t.Fatalf("P50 = %v, want 30ms", got.P50)
	}
	if got.P95 != 100*time.Millisecond {
		t.Fatalf("P95 = %v, want 100ms", got.P95)
	}
}

// TestToolTimingHistogramMultipleToolsIsolation 验证不同工具的样本互不串扰。
func TestToolTimingHistogramMultipleToolsIsolation(t *testing.T) {
	var h ToolTimingHistogram
	h.Record("read", 10*time.Millisecond)
	h.Record("read", 30*time.Millisecond)
	h.Record("write", 100*time.Millisecond)

	read := h.Stats("read")
	if read.Count != 2 || read.Total != 40*time.Millisecond || read.Max != 30*time.Millisecond {
		t.Fatalf("read 统计异常: %+v", read)
	}
	write := h.Stats("write")
	if write.Count != 1 || write.Total != 100*time.Millisecond || write.Min != 100*time.Millisecond {
		t.Fatalf("write 统计异常: %+v", write)
	}
	if missing := h.Stats("never"); missing != (ToolTimingStats{}) {
		t.Fatalf("未记录工具应返回零值统计, got %+v", missing)
	}
}

// TestToolTimingHistogramPercentiles 覆盖不同样本量下的 P50/P95，锁定最近秩算法。
func TestToolTimingHistogramPercentiles(t *testing.T) {
	cases := []struct {
		tool    string
		samples []time.Duration
		p50     time.Duration
		p95     time.Duration
	}{
		{"one", []time.Duration{5 * time.Millisecond}, 5 * time.Millisecond, 5 * time.Millisecond},
		{"two", []time.Duration{10 * time.Millisecond, 20 * time.Millisecond}, 10 * time.Millisecond, 20 * time.Millisecond},
		{"three", []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 30 * time.Millisecond}, 20 * time.Millisecond, 30 * time.Millisecond},
		{"four", []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 30 * time.Millisecond, 40 * time.Millisecond}, 20 * time.Millisecond, 40 * time.Millisecond},
	}

	var h ToolTimingHistogram
	for _, tc := range cases {
		for _, d := range tc.samples {
			h.Record(tc.tool, d)
		}
	}
	for _, tc := range cases {
		got := h.Stats(tc.tool)
		if got.P50 != tc.p50 || got.P95 != tc.p95 {
			t.Errorf("%s: P50/P95 = %v/%v, want %v/%v", tc.tool, got.P50, got.P95, tc.p50, tc.p95)
		}
	}
}

// TestToolTimingHistogramClampsNonPositive 验证负数与零被钳制为 0，不会污染 min/mean。
func TestToolTimingHistogramClampsNonPositive(t *testing.T) {
	var h ToolTimingHistogram
	h.Record("mixed", -5*time.Millisecond)
	h.Record("mixed", 0)
	h.Record("mixed", 10*time.Millisecond)

	got := h.Stats("mixed")
	if got.Count != 3 {
		t.Fatalf("Count = %d, want 3（钳制后仍计数）", got.Count)
	}
	if got.Min != 0 {
		t.Fatalf("Min = %v, want 0（负数/零已钳制）", got.Min)
	}
	if got.Total != 10*time.Millisecond || got.Max != 10*time.Millisecond {
		t.Fatalf("Total/Max = %v/%v, want 10ms/10ms", got.Total, got.Max)
	}
	if got.P50 != 0 || got.P95 != 10*time.Millisecond {
		t.Fatalf("P50/P95 = %v/%v, want 0/10ms", got.P50, got.P95)
	}

	var onlyNeg ToolTimingHistogram
	onlyNeg.Record("neg", -1*time.Nanosecond)
	neg := onlyNeg.Stats("neg")
	if neg.Count != 1 || neg.Min != 0 || neg.Max != 0 || neg.Total != 0 {
		t.Fatalf("纯负数样本统计异常: %+v", neg)
	}
}

// TestToolTimingHistogramTopSlow 验证按 P95 降序取前 n，并验证并列时的确定性别名顺序。
func TestToolTimingHistogramTopSlow(t *testing.T) {
	var h ToolTimingHistogram
	h.Record("slow", 90*time.Millisecond)
	h.Record("slow", 100*time.Millisecond)
	h.Record("mid", 50*time.Millisecond)
	h.Record("mid", 60*time.Millisecond)
	h.Record("fast", 10*time.Millisecond)
	h.Record("fast", 20*time.Millisecond)

	top2 := h.TopSlow(2)
	if len(top2) != 2 || top2[0].Tool != "slow" || top2[1].Tool != "mid" {
		t.Fatalf("TopSlow(2) = %+v, want [slow mid]", top2)
	}
	if top2[0].Stats.P95 != 100*time.Millisecond {
		t.Fatalf("slow P95 = %v, want 100ms", top2[0].Stats.P95)
	}

	all := h.TopSlow(10)
	if len(all) != 3 || all[0].Tool != "slow" || all[1].Tool != "mid" || all[2].Tool != "fast" {
		t.Fatalf("TopSlow(10) = %+v, want [slow mid fast]", all)
	}

	// 并列 P95 与 Total 时按工具名字典序升序，保证确定性。
	var tie ToolTimingHistogram
	for _, name := range []string{"zeta", "alpha", "beta"} {
		tie.Record(name, 10*time.Millisecond)
		tie.Record(name, 20*time.Millisecond)
	}
	for i := 0; i < 20; i++ {
		got := tie.TopSlow(3)
		if got[0].Tool != "alpha" || got[1].Tool != "beta" || got[2].Tool != "zeta" {
			t.Fatalf("并列时顺序不确定: %+v", got)
		}
	}
}

// TestToolTimingHistogramTopSlowNonPositiveN 验证 n<=0 返回空切片。
func TestToolTimingHistogramTopSlowNonPositiveN(t *testing.T) {
	var h ToolTimingHistogram
	h.Record("a", time.Millisecond)
	for _, n := range []int{0, -1, -100} {
		if got := h.TopSlow(n); len(got) != 0 {
			t.Fatalf("TopSlow(%d) = %+v, want 空", n, got)
		}
	}
}

// TestToolTimingHistogramZeroValue 验证零值各方法可用。
func TestToolTimingHistogramZeroValue(t *testing.T) {
	var h ToolTimingHistogram
	if got := h.Stats("missing"); got != (ToolTimingStats{}) {
		t.Fatalf("零值 Stats = %+v, want 全零", got)
	}
	if got := h.TopSlow(5); len(got) != 0 {
		t.Fatalf("零值 TopSlow = %+v, want 空", got)
	}
	h.Record("seed", 5*time.Millisecond)
	if got := h.Stats("seed"); got.Count != 1 || got.Total != 5*time.Millisecond {
		t.Fatalf("零值 Record 后统计异常: %+v", got)
	}
}

// TestToolTimingHistogramCJKToolNames 验证中文工具名可作为键参与统计与排序。
func TestToolTimingHistogramCJKToolNames(t *testing.T) {
	var h ToolTimingHistogram
	h.Record("读取文件", 30*time.Millisecond)
	h.Record("读取文件", 10*time.Millisecond)
	h.Record("写入文件", 5*time.Millisecond)

	if got := h.Stats("读取文件"); got.Count != 2 || got.Total != 40*time.Millisecond {
		t.Fatalf("中文工具统计异常: %+v", got)
	}
	top := h.TopSlow(2)
	if len(top) != 2 || top[0].Tool != "读取文件" || top[1].Tool != "写入文件" {
		t.Fatalf("中文工具 TopSlow = %+v, want [读取文件 写入文件]", top)
	}
}

// TestToolTimingHistogramDoesNotMutate 验证统计不修改入参切片，返回值也是副本。
func TestToolTimingHistogramDoesNotMutate(t *testing.T) {
	in := []time.Duration{30 * time.Millisecond, 10 * time.Millisecond, 20 * time.Millisecond}
	want := []time.Duration{in[0], in[1], in[2]}
	_ = statsFromSamples(in)
	for i := range want {
		if in[i] != want[i] {
			t.Fatalf("statsFromSamples 修改了入参: index %d = %v, want %v", i, in[i], want[i])
		}
	}

	var h ToolTimingHistogram
	h.Record("a", 10*time.Millisecond)
	h.Record("b", 20*time.Millisecond)

	// Stats 是值快照。
	s := h.Stats("a")
	s.Count = 999
	if got := h.Stats("a"); got.Count != 1 {
		t.Fatalf("修改 Stats 返回值影响了内部状态: %+v", got)
	}

	// TopSlow 返回的切片与元素都是副本。
	top := h.TopSlow(2)
	top[0].Tool = "mutated"
	top[0].Stats.Count = 999
	top[0].Stats.P95 = 999 * time.Hour
	again := h.TopSlow(2)
	for _, item := range again {
		if item.Tool == "mutated" || item.Stats.Count == 999 || item.Stats.P95 == 999*time.Hour {
			t.Fatalf("修改 TopSlow 返回值影响了内部状态: %+v", again)
		}
	}
}

// TestToolTimingHistogramConcurrent 在读写混合的并发场景下验证无数据竞争且计数自洽。
func TestToolTimingHistogramConcurrent(t *testing.T) {
	var h ToolTimingHistogram
	const recorders, readers, perWorker = 50, 20, 100
	start := make(chan struct{})
	var wg sync.WaitGroup

	for r := 0; r < recorders; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < perWorker; i++ {
				h.Record("concurrent", time.Duration(i%7+1)*time.Millisecond)
			}
		}()
	}
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < perWorker; i++ {
				_ = h.Stats("concurrent")
				_ = h.TopSlow(3)
			}
		}()
	}
	close(start)
	wg.Wait()

	got := h.Stats("concurrent")
	if got.Count != recorders*perWorker {
		t.Fatalf("Count = %d, want %d", got.Count, recorders*perWorker)
	}
	if got.Min < 0 || got.Max > 7*time.Millisecond {
		t.Fatalf("极值越界: Min=%v Max=%v", got.Min, got.Max)
	}
}
