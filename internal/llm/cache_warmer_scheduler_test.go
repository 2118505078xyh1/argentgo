package llm

import (
	"sync"
	"testing"
	"time"
)

var warmerBase = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

func enabledWarmer() *CacheWarmer {
	return &CacheWarmer{
		Enabled:          true,
		TTL:              time.Hour,
		WarmCost:         1,
		MissCost:         10,
		ContinuationProb: 0.5,
	}
}

// TestCacheWarmerTriggersInWindow pins that an enabled warmer fires only in
// the final stretch of the cache lifetime, before expiry.
func TestCacheWarmerTriggersInWindow(t *testing.T) {
	cw := enabledWarmer()
	cw.RecordRequest(warmerBase)
	cw.RecordIdle(warmerBase.Add(45 * time.Minute))

	if cw.ShouldWarm(warmerBase.Add(47 * time.Minute)) {
		t.Fatal("warm fired before the 80 percent window")
	}
	if !cw.ShouldWarm(warmerBase.Add(54 * time.Minute)) {
		t.Fatal("warm did not fire inside the window")
	}
	if cw.ShouldWarm(warmerBase.Add(time.Hour + time.Nanosecond)) {
		t.Fatal("warm fired after expiry")
	}
}

// TestCacheWarmerRecordIdleResetsIdleCap pins that reporting a fresh idle time
// at a turn boundary clears a stale idle age that would otherwise block a warm.
func TestCacheWarmerRecordIdleResetsIdleCap(t *testing.T) {
	cw := enabledWarmer()
	cw.RecordRequest(warmerBase)
	cw.RecordIdle(warmerBase)

	// Idle since the original request: 54 minutes exceeds the 30-minute cap.
	if cw.ShouldWarm(warmerBase.Add(54 * time.Minute)) {
		t.Fatal("warm fired while idle past the cap")
	}

	// The agent was active again and went idle recently; the cap resets.
	cw.RecordIdle(warmerBase.Add(50 * time.Minute))
	if !cw.ShouldWarm(warmerBase.Add(54 * time.Minute)) {
		t.Fatal("warm blocked after the idle clock was reset")
	}
}

// TestCacheWarmerUnprofitableDoesNotWarm pins the economics gate: a warm whose
// cost is not beaten by the expected avoided miss cost never fires.
func TestCacheWarmerUnprofitableDoesNotWarm(t *testing.T) {
	cw := enabledWarmer()
	cw.WarmCost = 6 // 10*0.5 = 5 expected saving, below the warm cost.
	cw.RecordRequest(warmerBase)
	cw.RecordIdle(warmerBase.Add(45 * time.Minute))

	if cw.ShouldWarm(warmerBase.Add(54 * time.Minute)) {
		t.Fatal("warm fired despite an unprofitable economics gate")
	}
}

// TestCacheWarmerDisabledDoesNotWarm pins that the zero value, and a warmer
// that has never observed a request, never fire.
func TestCacheWarmerDisabledDoesNotWarm(t *testing.T) {
	var zero CacheWarmer
	zero.RecordRequest(warmerBase)
	zero.RecordIdle(warmerBase)
	if zero.ShouldWarm(warmerBase.Add(54 * time.Minute)) {
		t.Fatal("zero-value (disabled) warmer fired")
	}

	cw := enabledWarmer()
	cw.RecordIdle(warmerBase) // idle observed, but no request ever refreshed cache
	if cw.ShouldWarm(warmerBase.Add(54 * time.Minute)) {
		t.Fatal("warmer fired without a recorded request")
	}
}

// TestCacheWarmerRequestRestartsWindow pins that a new real request re-times
// the lifetime and warm window (and clears the idle phase).
func TestCacheWarmerRequestRestartsWindow(t *testing.T) {
	cw := enabledWarmer()
	cw.RecordRequest(warmerBase)
	cw.RecordIdle(warmerBase.Add(45 * time.Minute))

	if !cw.ShouldWarm(warmerBase.Add(54 * time.Minute)) {
		t.Fatal("expected a warm before the new request")
	}

	fresh := warmerBase.Add(54 * time.Minute)
	cw.RecordRequest(fresh)
	if cw.ShouldWarm(fresh) {
		t.Fatal("warm fired immediately after a fresh request")
	}
	if cw.ShouldWarm(fresh.Add(47 * time.Minute)) {
		t.Fatal("warm fired before the restarted window")
	}
	if !cw.ShouldWarm(fresh.Add(54 * time.Minute)) {
		t.Fatal("warm did not fire in the restarted window")
	}
}

// TestCacheWarmerDefaultsUseConservativeValues pins that an enabled warmer
// with unset TTL and costs still behaves sensibly.
func TestCacheWarmerDefaultsUseConservativeValues(t *testing.T) {
	cw := &CacheWarmer{Enabled: true} // TTL and costs left at zero.
	cw.RecordRequest(warmerBase)
	cw.RecordIdle(warmerBase)

	if cw.ShouldWarm(warmerBase.Add(2 * time.Minute)) {
		t.Fatal("default warmer fired before the 80 percent window")
	}
	if !cw.ShouldWarm(warmerBase.Add(4*time.Minute + 30*time.Second)) {
		t.Fatal("default warmer did not fire inside the window")
	}
}

// TestCacheWarmerConcurrentUse exercises the mutex under concurrent access;
// it fails on a deadlock or panic and is clean under the race detector.
func TestCacheWarmerConcurrentUse(t *testing.T) {
	cw := enabledWarmer()
	cw.RecordRequest(warmerBase)
	cw.RecordIdle(warmerBase)

	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			now := warmerBase.Add(time.Duration(i) * time.Minute)
			cw.RecordIdle(now)
			cw.RecordRequest(now)
			_ = cw.ShouldWarm(now.Add(54 * time.Minute))
		}(i)
	}
	wg.Wait()
}
