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

// TestCacheWarmerTriggersInWindow 固定：启用的 warmer 只在缓存生存期的
// 最后阶段、过期之前触发。
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

// TestCacheWarmerRecordIdleResetsIdleCap 固定：在轮次边界报告新的空闲时间
// 会清除陈旧的空闲时长，否则它会阻止保活。
func TestCacheWarmerRecordIdleResetsIdleCap(t *testing.T) {
	cw := enabledWarmer()
	cw.RecordRequest(warmerBase)
	cw.RecordIdle(warmerBase)

	// 自最初请求以来一直空闲：54 分钟超过 30 分钟上限。
	if cw.ShouldWarm(warmerBase.Add(54 * time.Minute)) {
		t.Fatal("warm fired while idle past the cap")
	}

	// agent 再次活跃且最近才空闲；上限已重置。
	cw.RecordIdle(warmerBase.Add(50 * time.Minute))
	if !cw.ShouldWarm(warmerBase.Add(54 * time.Minute)) {
		t.Fatal("warm blocked after the idle clock was reset")
	}
}

// TestCacheWarmerUnprofitableDoesNotWarm 固定经济性门槛：若预期节省的
// 未命中成本不优于保活成本，则永不触发。
func TestCacheWarmerUnprofitableDoesNotWarm(t *testing.T) {
	cw := enabledWarmer()
	cw.WarmCost = 6 // 10*0.5 = 5 的预期节省，低于保活成本。
	cw.RecordRequest(warmerBase)
	cw.RecordIdle(warmerBase.Add(45 * time.Minute))

	if cw.ShouldWarm(warmerBase.Add(54 * time.Minute)) {
		t.Fatal("warm fired despite an unprofitable economics gate")
	}
}

// TestCacheWarmerDisabledDoesNotWarm 固定：零值以及
// 从未见过请求的 warmer 永不触发。
func TestCacheWarmerDisabledDoesNotWarm(t *testing.T) {
	var zero CacheWarmer
	zero.RecordRequest(warmerBase)
	zero.RecordIdle(warmerBase)
	if zero.ShouldWarm(warmerBase.Add(54 * time.Minute)) {
		t.Fatal("zero-value (disabled) warmer fired")
	}

	cw := enabledWarmer()
	cw.RecordIdle(warmerBase) // 观察到空闲，但从未有请求刷新过缓存
	if cw.ShouldWarm(warmerBase.Add(54 * time.Minute)) {
		t.Fatal("warmer fired without a recorded request")
	}
}

// TestCacheWarmerRequestRestartsWindow 固定：新的真实请求会重新计时
// 生存期和保活窗口（并清除空闲阶段）。
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

// TestCacheWarmerDefaultsUseConservativeValues 固定：启用的 warmer 即使
// 未设置 TTL 和成本也仍表现合理。
func TestCacheWarmerDefaultsUseConservativeValues(t *testing.T) {
	cw := &CacheWarmer{Enabled: true} // TTL 和成本保持为零。
	cw.RecordRequest(warmerBase)
	cw.RecordIdle(warmerBase)

	if cw.ShouldWarm(warmerBase.Add(2 * time.Minute)) {
		t.Fatal("default warmer fired before the 80 percent window")
	}
	if !cw.ShouldWarm(warmerBase.Add(4*time.Minute + 30*time.Second)) {
		t.Fatal("default warmer did not fire inside the window")
	}
}

// TestCacheWarmerConcurrentUse 在并发访问下演练互斥锁；
// 死锁或 panic 会使其失败，且在竞态检测器下干净通过。
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
