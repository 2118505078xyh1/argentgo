package app

import (
	"testing"
	"time"
)

func TestCacheWarmerEnabledDefaultOn(t *testing.T) {
	if !cacheWarmerEnabled(nil) {
		t.Fatal("nil env must default to enabled")
	}
	if !cacheWarmerEnabled(envFrom(nil)) {
		t.Fatal("empty env must default to enabled")
	}
	if !cacheWarmerEnabled(envFrom(map[string]string{"ARGENT_CACHE_WARM": "1"})) {
		t.Fatal("ARGENT_CACHE_WARM=1 keeps the warmer enabled")
	}
	if !cacheWarmerEnabled(envFrom(map[string]string{"ARGENT_CACHE_WARM": "yes"})) {
		t.Fatal("any value except 0 keeps the warmer enabled")
	}
	if cacheWarmerEnabled(envFrom(map[string]string{"ARGENT_CACHE_WARM": "0"})) {
		t.Fatal("ARGENT_CACHE_WARM=0 must disable")
	}
	if cacheWarmerEnabled(envFrom(map[string]string{"ARGENT_CACHE_WARM": " 0 "})) {
		t.Fatal("whitespace around 0 must be tolerated")
	}
}

func TestNewCacheWarmerExplicitlyDisabled(t *testing.T) {
	w := newCacheWarmer(false)
	if w == nil || w.Enabled {
		t.Fatalf("explicitly disabled warmer must exist and be disabled: %+v", w)
	}
	// 禁用的预热器即使在记录请求很久之后也保持惰性。
	now := time.Now()
	w.RecordRequest(now)
	if w.ShouldWarm(now.Add(time.Hour)) {
		t.Fatal("disabled warmer must never signal a warm")
	}
}

func TestCacheWarmerProbeRecordsBoundaries(t *testing.T) {
	probe := &cacheWarmerProbe{warmer: newCacheWarmer(true)}
	base := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

	// 没有记录请求时，没有可预热的内容。
	if probe.shouldWarm(base) {
		t.Fatal("warmer signalled before any request")
	}

	probe.recordRequest(base)
	// 在预热窗口内（默认 5m TTL 的 80% = 4m，按默认
	// 经济策略）应当预热；探针只作答，从不发送任何内容。
	if !probe.shouldWarm(base.Add(4 * time.Minute)) {
		t.Fatal("warmer did not signal inside the warm window")
	}

	// 空闲标记会重启空闲时长上限，但不会清除请求。
	probe.recordIdle(base.Add(4 * time.Minute))
	if !probe.shouldWarm(base.Add(4 * time.Minute)) {
		t.Fatal("idle marker must not cancel a live warm window")
	}
}

func TestCacheWarmerProbeNilSafe(t *testing.T) {
	var nilProbe *cacheWarmerProbe
	now := time.Now()
	nilProbe.recordRequest(now)
	nilProbe.recordIdle(now)
	if nilProbe.shouldWarm(now) {
		t.Fatal("nil probe must never signal a warm")
	}
	empty := &cacheWarmerProbe{}
	empty.recordRequest(now)
	empty.recordIdle(now)
	if empty.shouldWarm(now) {
		t.Fatal("probe with no warmer must never signal a warm")
	}
}
