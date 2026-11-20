package app

import (
	"testing"
	"time"
)

func TestCacheWarmerEnabledOptIn(t *testing.T) {
	if cacheWarmerEnabled(nil) {
		t.Fatal("nil env must default to disabled")
	}
	if cacheWarmerEnabled(envFrom(nil)) {
		t.Fatal("empty env must default to disabled")
	}
	if !cacheWarmerEnabled(envFrom(map[string]string{"ARGENT_CACHE_WARM": "1"})) {
		t.Fatal("ARGENT_CACHE_WARM=1 must opt in")
	}
	if !cacheWarmerEnabled(envFrom(map[string]string{"ARGENT_CACHE_WARM": " 1 "})) {
		t.Fatal("whitespace around 1 must be tolerated")
	}
	if cacheWarmerEnabled(envFrom(map[string]string{"ARGENT_CACHE_WARM": "yes"})) {
		t.Fatal("only the literal 1 opts in")
	}
}

func TestNewCacheWarmerDefaultsDisabled(t *testing.T) {
	w := newCacheWarmer(false)
	if w == nil || w.Enabled {
		t.Fatalf("default warmer must exist and be disabled: %+v", w)
	}
	// A disabled warmer stays inert even long after a recorded request.
	now := time.Now()
	w.RecordRequest(now)
	if w.ShouldWarm(now.Add(time.Hour)) {
		t.Fatal("disabled warmer must never signal a warm")
	}
}

func TestCacheWarmerProbeRecordsBoundaries(t *testing.T) {
	probe := &cacheWarmerProbe{warmer: newCacheWarmer(true)}
	base := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

	// With no recorded request there is nothing to warm.
	if probe.shouldWarm(base) {
		t.Fatal("warmer signalled before any request")
	}

	probe.recordRequest(base)
	// Inside the warm window (80% of the 5m default TTL = 4m, with the default
	// economics) a warm is due; the probe only answers, it never sends anything.
	if !probe.shouldWarm(base.Add(4 * time.Minute)) {
		t.Fatal("warmer did not signal inside the warm window")
	}

	// An idle marker restarts the idle-age cap but does not clear the request.
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
