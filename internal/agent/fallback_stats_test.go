package agent

import (
	"fmt"
	"math"
	"reflect"
	"sync"
	"testing"
)

// fallbackStat 是 Snapshot 结果结构在测试中的别名，避免重复书写匿名结构体。
type fallbackStat struct {
	Attempts  int
	Successes int
	Rate      float64
}

func TestFallbackStatsSuccessRate(t *testing.T) {
	// 成功率 = 成功数 / 尝试数；钳制前的正常比值应精确。
	var s FallbackStats
	const reason FallbackReason = "status"
	for i := 0; i < 4; i++ {
		s.Record(reason, false)
	}
	s.Record(reason, true)
	s.Record(reason, true)

	got := s.Snapshot()[reason]
	want := fallbackStat{Attempts: 6, Successes: 2, Rate: 1.0 / 3.0}
	if got.Attempts != want.Attempts || got.Successes != want.Successes || got.Rate != want.Rate {
		t.Fatalf("Snapshot()[%q] = %+v, want %+v", reason, got, want)
	}
}

func TestFallbackStatsRateClamped(t *testing.T) {
	// 成功数多于尝试数（误记）时 Rate 钳制到 1，且保持有限值。
	var s FallbackStats
	s.Record("kind", true)
	for i := 0; i < 10; i++ {
		s.Record("kind", true)
	}
	got := s.Snapshot()["kind"]
	if got.Rate != 1 || math.IsNaN(got.Rate) || math.IsInf(got.Rate, 0) {
		t.Fatalf("Rate = %v, want clamped finite 1", got.Rate)
	}
	// 正常比值仍应精确。
	s.Record("half", true)
	s.Record("half", false)
	if got := s.Snapshot()["half"].Rate; got != 0.5 {
		t.Fatalf("half Rate = %v, want 0.5", got)
	}
}

func TestFallbackStatsReasonsIsolated(t *testing.T) {
	// 不同切换原因各自独立累计，改写一种不影响其它原因。
	var s FallbackStats
	s.Record("rate_limit", false)
	s.Record("rate_limit", true)
	s.Record("timeout", false)
	s.Record("timeout", false)
	s.Record("timeout", false)
	s.Record("unreachable", true)
	s.Record("unreachable", true)

	snap := s.Snapshot()
	want := map[FallbackReason]fallbackStat{
		"rate_limit":  {Attempts: 2, Successes: 1, Rate: 0.5},
		"timeout":     {Attempts: 3, Successes: 0, Rate: 0},
		"unreachable": {Attempts: 2, Successes: 2, Rate: 1},
	}
	got := map[FallbackReason]fallbackStat{}
	for reason, v := range snap {
		got[reason] = fallbackStat(v)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Snapshot() = %+v, want %+v", got, want)
	}

	// 继续写 "timeout" 不应改变另外两种。
	s.Record("timeout", true)
	after := s.Snapshot()
	if v := after["timeout"]; v.Attempts != 4 || v.Successes != 1 || v.Rate != 0.25 {
		t.Fatalf("timeout stats = %+v, want 4 attempts/1 success", v)
	}
	if v := after["rate_limit"]; v != fallbackStat(want["rate_limit"]) {
		t.Fatalf("rate_limit changed = %+v, want %+v", v, want["rate_limit"])
	}
	if v := after["unreachable"]; v != fallbackStat(want["unreachable"]) {
		t.Fatalf("unreachable changed = %+v, want %+v", v, want["unreachable"])
	}
}

func TestFallbackStatsZeroAttemptsNoNaN(t *testing.T) {
	// 零值与 nil 接收者的快照都是非 nil 空 map。
	var s FallbackStats
	if snap := s.Snapshot(); snap == nil || len(snap) != 0 {
		t.Fatalf("zero-value Snapshot() = %v, want non-nil empty map", snap)
	}
	var nilPtr *FallbackStats
	if snap := nilPtr.Snapshot(); snap == nil || len(snap) != 0 {
		t.Fatalf("nil Snapshot() = %v, want non-nil empty map", snap)
	}

	// nil 接收者的写方法同样安全。
	nilPtr.Record("kind", true)
	nilPtr.Record("kind", false)

	// 仅登记过但无任何尝试的原因不会出现（Record 才创建条目）。
	// 这里直接验证 fallbackRate 在零尝试时不产生 NaN/Inf。
	if got := fallbackRate(0, 0); got != 0 || math.IsNaN(got) || math.IsInf(got, 0) {
		t.Fatalf("fallbackRate(0,0) = %v, want 0", got)
	}
	for _, rate := range []float64{fallbackRate(0, 5), fallbackRate(1, 0), fallbackRate(4, 2)} {
		if math.IsNaN(rate) || math.IsInf(rate, 0) || rate < 0 || rate > 1 {
			t.Fatalf("invalid Rate %v", rate)
		}
	}
}

func TestFallbackStatsSnapshotIsCopy(t *testing.T) {
	var s FallbackStats
	s.Record("a", true)
	s.Record("b", false)

	snap := s.Snapshot()
	// 调用方修改快照不得影响内部状态。
	delete(snap, "b")
	snap["a"] = struct {
		Attempts  int
		Successes int
		Rate      float64
	}{Attempts: 99, Successes: 99, Rate: 1}
	snap["c"] = struct {
		Attempts  int
		Successes int
		Rate      float64
	}{Attempts: 9, Successes: 9, Rate: 1}

	after := s.Snapshot()
	want := map[FallbackReason]fallbackStat{
		"a": {Attempts: 1, Successes: 1, Rate: 1},
		"b": {Attempts: 1, Successes: 0, Rate: 0},
	}
	got := map[FallbackReason]fallbackStat{}
	for reason, v := range after {
		got[reason] = fallbackStat(v)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Snapshot() after mutation = %+v, want %+v", got, want)
	}
	if _, ok := after["c"]; ok {
		t.Fatal("mutating snapshot leaked a new key into internal state")
	}

	// 两次快照彼此独立。
	first := s.Snapshot()
	second := s.Snapshot()
	first["a"] = struct {
		Attempts  int
		Successes int
		Rate      float64
	}{}
	if v := second["a"]; v.Attempts != 1 || v.Successes != 1 || v.Rate != 1 {
		t.Fatalf("mutating first snapshot changed second: %+v", v)
	}
}

func TestFallbackStatsCJKReason(t *testing.T) {
	// 切换原因允许任意字符串，包括 CJK 文本；原样作为 map 键保存。
	var s FallbackStats
	const reason FallbackReason = "限流"
	s.Record(reason, true)
	s.Record(reason, false)
	s.Record("服务不可用", false)
	s.Record("网络超时", true)

	snap := s.Snapshot()
	if v := snap[reason]; v.Attempts != 2 || v.Successes != 1 || v.Rate != 0.5 {
		t.Fatalf("Snapshot()[%q] = %+v, want 2/1/0.5", reason, v)
	}
	if v := snap["服务不可用"]; v.Attempts != 1 || v.Successes != 0 || v.Rate != 0 {
		t.Fatalf("Snapshot()[服务不可用] = %+v, want 1/0/0", v)
	}
	if v := snap["网络超时"]; v.Attempts != 1 || v.Successes != 1 || v.Rate != 1 {
		t.Fatalf("Snapshot()[网络超时] = %+v, want 1/1/1", v)
	}
	if len(snap) != 3 {
		t.Fatalf("Snapshot() has %d reasons, want 3", len(snap))
	}
}

func TestFallbackStatsDoesNotMutateInput(t *testing.T) {
	// Record 按值接收 reason 与 succeeded，不得修改调用方持有的变量。
	reason := FallbackReason("原始原因")
	succeeded := true
	var s FallbackStats
	s.Record(reason, succeeded)
	succeeded = false
	s.Record(reason, succeeded)

	if reason != "原始原因" {
		t.Fatalf("reason mutated to %q", reason)
	}
	if got := s.Snapshot()[reason]; got.Attempts != 2 || got.Successes != 1 || got.Rate != 0.5 {
		t.Fatalf("Snapshot()[reason] = %+v, want 2/1/0.5", got)
	}
	// 复用同一变量作为键仍命中同一条目，未被 Record 改写。
	s.Record(reason, true)
	if got := s.Snapshot()[reason].Successes; got != 2 {
		t.Fatalf("Successes = %d, want 2", got)
	}
}

func TestFallbackStatsDeterministic(t *testing.T) {
	// 每种原因的操作序列固定，仅跨原因的交错顺序不同；
	// 快照应完全一致，与写入的全局顺序无关。
	ops := map[FallbackReason]string{
		"status":  "ssf",
		"timeout": "sff",
		"限流":      "s",
	}
	apply := func(s *FallbackStats, reason FallbackReason, seq string) {
		for _, op := range seq {
			s.Record(reason, op == 's')
		}
	}

	var sequential FallbackStats
	reasons := []FallbackReason{"status", "timeout", "限流"}
	for _, reason := range reasons {
		apply(&sequential, reason, ops[reason])
	}

	var interleaved FallbackStats
	for step := 0; step < 3; step++ {
		for _, reason := range reasons {
			if step < len(ops[reason]) {
				apply(&interleaved, reason, string(ops[reason][step]))
			}
		}
	}

	want := sequential.Snapshot()
	if got := interleaved.Snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("interleaved Snapshot() = %+v, want %+v", got, want)
	}
	// 反复读取同一状态，Rate 作为整数计数的确定性函数不应漂移。
	for i := 0; i < 10; i++ {
		if got := sequential.Snapshot(); !reflect.DeepEqual(got, want) {
			t.Fatalf("Snapshot() #%d = %+v, want %+v", i, got, want)
		}
	}
}

func TestFallbackStatsConcurrentAccess(t *testing.T) {
	// 每个 goroutine 只写自己的原因，同时让 Snapshot 与写入并发，
	// 由竞态检测器验证锁的正确性。
	var s FallbackStats
	const workers = 32
	const iterations = 100
	errCh := make(chan string, workers)
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			reason := FallbackReason(fmt.Sprintf("reason-%d", id))
			for j := 0; j < iterations; j++ {
				s.Record(reason, true)
				s.Record(reason, false)
				_ = s.Snapshot()
			}

			got := s.Snapshot()[reason]
			if got.Attempts != iterations*2 || got.Successes != iterations || got.Rate != 0.5 {
				errCh <- fmt.Sprintf("%q stats = %+v, want %d attempts/%d successes",
					reason, got, iterations*2, iterations)
			}
		}(i)
	}

	wg.Wait()
	close(errCh)
	for msg := range errCh {
		t.Error(msg)
	}
	if snap := s.Snapshot(); len(snap) != workers {
		t.Fatalf("Snapshot() has %d reasons, want %d", len(snap), workers)
	}
}
