package agent

import (
	"sync"
	"testing"
)

// TestInvalidArgBreakerTripsAtDefaultThreshold 锁定核心机制：只有无效参数结果才计数，一轮中的第三次就会发出停止信号。
// （接上行）
func TestInvalidArgBreakerTripsAtDefaultThreshold(t *testing.T) {
	b := NewInvalidArgBreaker(0) // 默认阈值
	if got := b.Threshold(); got != DefaultInvalidArgThreshold {
		t.Fatalf("Threshold() = %d, want default %d", got, DefaultInvalidArgThreshold)
	}

	// 低于阈值：不停止。
	for i := 1; i < DefaultInvalidArgThreshold; i++ {
		if stop := b.RecordInvalidArgs(); stop {
			t.Fatalf("stopped after %d invalid-argument outcomes, want < %d", i, DefaultInvalidArgThreshold)
		}
		if got := b.Count(); got != i {
			t.Fatalf("Count() = %d, want %d", got, i)
		}
		if b.ShouldStop() {
			t.Fatalf("ShouldStop() true after %d invalid-argument outcomes", i)
		}
	}

	// 达到阈值：停止，并保持锁存直到重置或进展事件。
	if stop := b.RecordInvalidArgs(); !stop {
		t.Fatalf("did not stop at %d invalid-argument outcomes", DefaultInvalidArgThreshold)
	}
	if !b.ShouldStop() {
		t.Fatal("ShouldStop() = false at the threshold")
	}
	if stop := b.Observe(InvalidArgOutcomeNone); !stop {
		t.Fatal("neutral outcome must not clear a latched stop")
	}
}

// TestInvalidArgBreakerCustomThreshold 覆盖正阈值以及收紧规则（非正 -> 默认，超过上限 -> 上限）。
// （接上行）
func TestInvalidArgBreakerCustomThreshold(t *testing.T) {
	tests := []struct {
		name      string
		requested int
		want      int
	}{
		{"default on zero", 0, DefaultInvalidArgThreshold},
		{"default on negative", -3, DefaultInvalidArgThreshold},
		{"explicit one", 1, 1},
		{"explicit five", 5, 5},
		{"clamped to cap", InvalidArgBreakerCap + 100, InvalidArgBreakerCap},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := NewInvalidArgBreaker(tt.requested)
			if got := b.Threshold(); got != tt.want {
				t.Fatalf("Threshold() = %d, want %d", got, tt.want)
			}
			// 无论有效阈值是多少，记录那么多次无效参数都必须使熔断器跳闸。
			// （接上行）
			for i := 0; i < tt.want; i++ {
				if stop := b.RecordInvalidArgs(); stop && i < tt.want-1 {
					t.Fatalf("stopped early at %d of %d", i+1, tt.want)
				}
			}
			if !b.ShouldStop() {
				t.Fatalf("breaker did not trip after %d invalid-argument outcomes", tt.want)
			}
		})
	}
}

// TestInvalidArgBreakerSuccessClears 检查成功的工具调用会立即清除计数，重新武装熔断器。
// （接上行）
func TestInvalidArgBreakerSuccessClears(t *testing.T) {
	b := NewInvalidArgBreaker(3)
	b.RecordInvalidArgs()
	b.RecordInvalidArgs()
	if got := b.Count(); got != 2 {
		t.Fatalf("precondition Count() = %d, want 2", got)
	}

	if stop := b.Observe(InvalidArgOutcomeSuccess); stop {
		t.Fatal("a successful call must never stop the turn")
	}
	if got := b.Count(); got != 0 {
		t.Fatalf("Count() after success = %d, want 0", got)
	}
	if b.ShouldStop() {
		t.Fatal("ShouldStop() true after a successful call")
	}

	// 清除后连续序列从头开始。
	if stop := b.RecordInvalidArgs(); stop {
		t.Fatal("breaker did not re-arm after success")
	}
	if got := b.Count(); got != 1 {
		t.Fatalf("Count() after re-arm = %d, want 1", got)
	}
}

// TestInvalidArgBreakerOtherFailureDoesNotCount 锁定非校验失败既不增加计数也不保持锁存：按「非参数失败清除」规则，它会清除计数。
// （接上行）
// （接上行）
func TestInvalidArgBreakerOtherFailureDoesNotCount(t *testing.T) {
	b := NewInvalidArgBreaker(1)
	// 单次非参数失败不得触发阈值为 1 的熔断器。
	if stop := b.Observe(InvalidArgOutcomeOtherFailure); stop {
		t.Fatal("non-argument failure must not stop the turn")
	}
	if got := b.Count(); got != 0 {
		t.Fatalf("Count() after non-argument failure = %d, want 0", got)
	}

	// 非参数失败也会清除累积的无效参数计数。
	b.RecordInvalidArgs() // count = 1，会触发阈值 1
	if !b.ShouldStop() {
		t.Fatal("precondition: breaker should be tripped")
	}
	if stop := b.Observe(InvalidArgOutcomeOtherFailure); stop {
		t.Fatal("non-argument failure must clear a latched stop")
	}
	if got := b.Count(); got != 0 {
		t.Fatalf("Count() after non-argument failure = %d, want 0", got)
	}
}

// TestInvalidArgBreakerCap 检查计数器在上限处饱和而非无界增长，同时停止信号保持锁存。
// （接上行）
func TestInvalidArgBreakerCap(t *testing.T) {
	b := NewInvalidArgBreaker(InvalidArgBreakerCap + 100) // 阈值被收紧到上限
	for i := 0; i < InvalidArgBreakerCap*2; i++ {
		b.RecordInvalidArgs()
	}
	if got := b.Count(); got != InvalidArgBreakerCap {
		t.Fatalf("Count() = %d, want cap %d", got, InvalidArgBreakerCap)
	}
	if !b.ShouldStop() {
		t.Fatal("ShouldStop() = false at the cap")
	}

	// 重置使计数器可以从零再次增长。
	b.Reset()
	if got := b.Count(); got != 0 {
		t.Fatalf("Count() after Reset = %d, want 0", got)
	}
	if b.ShouldStop() {
		t.Fatal("ShouldStop() true after Reset")
	}
}

// TestInvalidArgBreakerObserveTable 在一处演练每种结果，包括只有无效参数才会推进计数。
// （接上行）
func TestInvalidArgBreakerObserveTable(t *testing.T) {
	tests := []struct {
		name     string
		outcome  InvalidArgOutcome
		wantStop bool
		wantCnt  int
	}{
		{"invalid args counts", InvalidArgOutcomeInvalidArgs, true, 1},
		{"success clears", InvalidArgOutcomeSuccess, false, 0},
		{"other failure clears", InvalidArgOutcomeOtherFailure, false, 0},
		{"none is neutral", InvalidArgOutcomeNone, false, 0},
		{"unknown is neutral", InvalidArgOutcome(99), false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := NewInvalidArgBreaker(1)
			if stop := b.Observe(tt.outcome); stop != tt.wantStop {
				t.Errorf("Observe(%d) stop = %v, want %v", tt.outcome, stop, tt.wantStop)
			}
			if got := b.Count(); got != tt.wantCnt {
				t.Errorf("Count() = %d, want %d", got, tt.wantCnt)
			}
		})
	}
}

// TestInvalidArgBreakerNilReceiver 锁定零值/nil 安全性，使调用方可以持有一个从未构造的熔断器，而无需在每次调用时做防范。
// （接上行）
func TestInvalidArgBreakerNilReceiver(t *testing.T) {
	var b *InvalidArgBreaker
	if stop := b.RecordInvalidArgs(); stop {
		t.Error("nil RecordInvalidArgs() returned stop")
	}
	if stop := b.Observe(InvalidArgOutcomeSuccess); stop {
		t.Error("nil Observe() returned stop")
	}
	if b.ShouldStop() {
		t.Error("nil ShouldStop() = true")
	}
	if got := b.Count(); got != 0 {
		t.Errorf("nil Count() = %d, want 0", got)
	}
	if got := b.Threshold(); got != 0 {
		t.Errorf("nil Threshold() = %d, want 0", got)
	}
	b.Reset() // 不得 panic
}

// TestInvalidArgBreakerConcurrentObserve 让许多 goroutine 执行 Observe/Count/Reset。带 -race 时能捕获数据竞态；不带时精确计数仍能断言在上限之前没有丢失任何更新。
// （接上行）
// （接上行）
func TestInvalidArgBreakerConcurrentObserve(t *testing.T) {
	const (
		goroutines = 16
		perRoutine = 20
	)
	b := NewInvalidArgBreaker(InvalidArgBreakerCap + 100) // 高阈值，受限的计数
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perRoutine; i++ {
				b.Observe(InvalidArgOutcomeInvalidArgs)
				_ = b.Count()
				_ = b.ShouldStop()
			}
		}()
	}
	wg.Wait()
	if got, want := b.Count(), InvalidArgBreakerCap; got != want {
		t.Fatalf("Count() = %d, want %d (cap)", got, want)
	}
}
