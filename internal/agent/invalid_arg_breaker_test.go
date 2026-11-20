package agent

import (
	"sync"
	"testing"
)

// TestInvalidArgBreakerTripsAtDefaultThreshold pins the core mechanism: only
// invalid-argument outcomes count, and the third one in a turn signals a stop.
func TestInvalidArgBreakerTripsAtDefaultThreshold(t *testing.T) {
	b := NewInvalidArgBreaker(0) // default threshold
	if got := b.Threshold(); got != DefaultInvalidArgThreshold {
		t.Fatalf("Threshold() = %d, want default %d", got, DefaultInvalidArgThreshold)
	}

	// Below the threshold: no stop.
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

	// At the threshold: stop, and it stays latched until a reset/progress event.
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

// TestInvalidArgBreakerCustomThreshold covers positive thresholds and the
// clamping rules (non-positive -> default, above cap -> cap).
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
			// Whatever the effective threshold, recording that many invalid
			// arguments must trip the breaker.
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

// TestInvalidArgBreakerSuccessClears checks that a successful tool call
// immediately clears the count, re-arming the breaker.
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

	// The streak restarts from scratch after the clear.
	if stop := b.RecordInvalidArgs(); stop {
		t.Fatal("breaker did not re-arm after success")
	}
	if got := b.Count(); got != 1 {
		t.Fatalf("Count() after re-arm = %d, want 1", got)
	}
}

// TestInvalidArgBreakerOtherFailureDoesNotCount pins that a non-validation
// failure neither increments the count nor leaves it latched: it clears, per
// the "non-argument failure clears" rule.
func TestInvalidArgBreakerOtherFailureDoesNotCount(t *testing.T) {
	b := NewInvalidArgBreaker(1)
	// A single non-argument failure must not trip a threshold-1 breaker.
	if stop := b.Observe(InvalidArgOutcomeOtherFailure); stop {
		t.Fatal("non-argument failure must not stop the turn")
	}
	if got := b.Count(); got != 0 {
		t.Fatalf("Count() after non-argument failure = %d, want 0", got)
	}

	// A non-argument failure also clears an accumulated invalid-argument count.
	b.RecordInvalidArgs() // count = 1, would trip threshold 1
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

// TestInvalidArgBreakerCap checks that the counter saturates at the cap instead
// of growing without bound, while the stop signal stays latched.
func TestInvalidArgBreakerCap(t *testing.T) {
	b := NewInvalidArgBreaker(InvalidArgBreakerCap + 100) // threshold clamped to cap
	for i := 0; i < InvalidArgBreakerCap*2; i++ {
		b.RecordInvalidArgs()
	}
	if got := b.Count(); got != InvalidArgBreakerCap {
		t.Fatalf("Count() = %d, want cap %d", got, InvalidArgBreakerCap)
	}
	if !b.ShouldStop() {
		t.Fatal("ShouldStop() = false at the cap")
	}

	// A reset frees the counter to grow again from zero.
	b.Reset()
	if got := b.Count(); got != 0 {
		t.Fatalf("Count() after Reset = %d, want 0", got)
	}
	if b.ShouldStop() {
		t.Fatal("ShouldStop() true after Reset")
	}
}

// TestInvalidArgBreakerObserveTable exercises every outcome in one place,
// including that only invalid arguments advance the count.
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

// TestInvalidArgBreakerNilReceiver pins zero-value/nil safety so a caller can
// hold a breaker that was never constructed without guarding every call.
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
	b.Reset() // must not panic
}

// TestInvalidArgBreakerConcurrentObserve drives many goroutines through
// Observe/Count/Reset. With -race this catches data races; without it the exact
// count still asserts no update was lost before the cap.
func TestInvalidArgBreakerConcurrentObserve(t *testing.T) {
	const (
		goroutines = 16
		perRoutine = 20
	)
	b := NewInvalidArgBreaker(InvalidArgBreakerCap + 100) // high threshold, capped count
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
