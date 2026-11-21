package agent

import (
	"sync"
	"testing"
	"time"
)

// endPhaseWithDuration 注入一个“过去 d 时刻”的开始时间再调用 EndPhase，
// 让耗时断言确定可期（time.Since 只会多出极小的调度误差），避免依赖 sleep。
func endPhaseWithDuration(t *testing.T, tt *TurnTiming, p TurnPhase, d time.Duration) {
	t.Helper()
	mu := tt.ensure()
	mu.Lock()
	tt.starts[p] = time.Now().Add(-d)
	mu.Unlock()
	tt.EndPhase(p)
}

// assertDurationNear 断言 got 落在 [want, want+slop) 区间内。
func assertDurationNear(t *testing.T, got, want time.Duration, what string) {
	t.Helper()
	const slop = time.Second
	if got < want || got >= want+slop {
		t.Fatalf("%s = %v, want in [%v, %v)", what, got, want, want+slop)
	}
}

// TestTurnTimingPhaseAccumulation 验证各阶段分别累加、同一阶段多次结算会叠加。
func TestTurnTimingPhaseAccumulation(t *testing.T) {
	var tt TurnTiming

	endPhaseWithDuration(t, &tt, phaseInference, 10*time.Millisecond)
	endPhaseWithDuration(t, &tt, phaseTools, 20*time.Millisecond)
	// 推理阶段再次发生，应叠加到之前的 10ms 上。
	endPhaseWithDuration(t, &tt, phaseInference, 5*time.Millisecond)

	snap := tt.Snapshot()
	assertDurationNear(t, snap.phases[phaseInference], 15*time.Millisecond, "phases[inference]")
	assertDurationNear(t, snap.phases[phaseTools], 20*time.Millisecond, "phases[tools]")
	if _, ok := snap.phases[phaseCompaction]; ok {
		t.Fatal("未结算的 compaction 阶段不应出现在 phases 中")
	}
}

// TestTurnTimingTotal 验证 Total 等于各阶段之和，并且空计时为 0。
func TestTurnTimingTotal(t *testing.T) {
	var empty TurnTiming
	if got := empty.Total(); got != 0 {
		t.Fatalf("空 TurnTiming.Total() = %v, want 0", got)
	}

	var tt TurnTiming
	endPhaseWithDuration(t, &tt, phaseInference, 10*time.Millisecond)
	endPhaseWithDuration(t, &tt, phaseTools, 20*time.Millisecond)
	endPhaseWithDuration(t, &tt, phaseOverhead, 5*time.Millisecond)
	assertDurationNear(t, tt.Total(), 35*time.Millisecond, "Total()")
}

// TestTurnTimingEndWithoutBeginIsNoop 验证孤立的 EndPhase 不会污染统计。
func TestTurnTimingEndWithoutBeginIsNoop(t *testing.T) {
	var tt TurnTiming
	tt.EndPhase(phaseUserWait)
	if got := tt.Total(); got != 0 {
		t.Fatalf("孤立 EndPhase 后 Total() = %v, want 0", got)
	}
}

// TestTurnTimingCompleteIncomplete 验证 complete 标志与不完整原因的维护。
func TestTurnTimingCompleteIncomplete(t *testing.T) {
	var tt TurnTiming
	if tt.Snapshot().complete {
		t.Fatal("零值不应是 complete")
	}

	tt.MarkComplete()
	if !tt.Snapshot().complete {
		t.Fatal("MarkComplete 后应为 complete")
	}

	tt.MarkIncomplete("budget exhausted")
	snap := tt.Snapshot()
	if snap.complete {
		t.Fatal("MarkIncomplete 后应不再 complete")
	}
	if len(snap.incompleteReasons) != 1 || snap.incompleteReasons[0] != "budget exhausted" {
		t.Fatalf("incompleteReasons = %v, want [budget exhausted]", snap.incompleteReasons)
	}

	tt.MarkIncomplete("") // 空原因也要保留信号
	if got := tt.Snapshot().incompleteReasons; len(got) != 2 || got[1] != "" {
		t.Fatalf("incompleteReasons = %v, want 追加空原因后的 2 条", got)
	}
}

// TestTurnTimingSnapshotIsCopy 验证快照与原对象互相隔离。
func TestTurnTimingSnapshotIsCopy(t *testing.T) {
	var tt TurnTiming
	endPhaseWithDuration(t, &tt, phaseInference, 10*time.Millisecond)
	tt.MarkIncomplete("original")

	snap := tt.Snapshot()
	originalTotal := tt.Total()

	// 修改原对象，快照不应变化。
	endPhaseWithDuration(t, &tt, phaseTools, 50*time.Millisecond)
	tt.MarkComplete()
	tt.MarkIncomplete("later")
	if got := snap.Total(); got != originalTotal {
		t.Fatalf("原对象变化后快照 Total() = %v, want %v", got, originalTotal)
	}
	if s := snap.Snapshot(); s.complete || len(s.incompleteReasons) != 1 {
		t.Fatalf("原对象变化污染了快照: %+v", s)
	}

	// 修改快照的 map 与 slice，原对象不应变化。
	snap.phases[phaseInference] = 999 * time.Millisecond
	snap.incompleteReasons[0] = "mutated"
	origin := tt.Snapshot()
	if origin.phases[phaseInference] >= 999*time.Millisecond {
		t.Fatal("修改快照的 phases 不应影响原对象")
	}
	if origin.incompleteReasons[0] == "mutated" {
		t.Fatal("修改快照的 incompleteReasons 不应影响原对象")
	}
}

// TestTurnTimingZeroValue 验证零值各方法的可用性。
func TestTurnTimingZeroValue(t *testing.T) {
	var tt TurnTiming
	if got := tt.Total(); got != 0 {
		t.Fatalf("零值 Total() = %v, want 0", got)
	}
	snap := tt.Snapshot()
	if snap.phases == nil || snap.starts == nil || snap.incompleteReasons != nil {
		t.Fatalf("零值快照字段异常: %+v", snap)
	}
	tt.BeginPhase(phaseCompaction)
	if got := tt.Total(); got != 0 {
		t.Fatalf("仅 BeginPhase 后 Total() = %v, want 0", got)
	}
	tt.EndPhase(phaseCompaction)
	if snap := tt.Snapshot(); len(snap.starts) != 0 {
		t.Fatal("结算后 starts 应被清空")
	}
}

// TestTurnTimingConcurrent 在多 goroutine 下并发调用全部方法，验证无数据竞争/死锁且结果自洽。
func TestTurnTimingConcurrent(t *testing.T) {
	var tt TurnTiming
	const workers = 100

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// 故意在零值上直接并发首次使用，覆盖 ensure 的惰性初始化路径。
			phase := phaseTools
			if i%2 == 0 {
				phase = phaseInference
			}
			tt.BeginPhase(phase)
			tt.EndPhase(phase)
			tt.MarkIncomplete("concurrent")
			_ = tt.Total()
			_ = tt.Snapshot()
		}(i)
	}
	wg.Wait()

	snap := tt.Snapshot()
	if len(snap.incompleteReasons) != workers {
		t.Fatalf("incompleteReasons 数量 = %d, want %d", len(snap.incompleteReasons), workers)
	}
	// 不断言 Total > 0：Windows 时钟分辨率较粗，紧凑并发下 Begin/End 可能落在同一 tick，
	// 使 time.Since 返回 0。但至少应有一个阶段被 EndPhase 结算出键（值可能为 0）。
	if len(snap.phases) == 0 {
		t.Fatal("并发结算后至少应记录一个阶段")
	}
	if snap.complete {
		t.Fatal("并发中未调用 MarkComplete，complete 应为假")
	}
}
