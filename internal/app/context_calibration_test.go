package app

import (
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/agent"
)

func TestCalibrationFactorClamp(t *testing.T) {
	cases := []struct {
		measured, estimated, want int
	}{
		{120000, 60000, 2}, // 实测 120k / 估算 60k → 2
		{300000, 60000, 4}, // 低估严重 → 钳到上限 4
		{60000, 120000, 1}, // 高估 → 不下调，钳到下限 1
		{0, 60000, 1},      // 无实测 → 1
		{120000, 0, 1},     // 无估算 → 1
		{-1, 60000, 1},     // 非法实测 → 1
	}
	for _, tc := range cases {
		if got := agent.CalibrationFactor(tc.measured, tc.estimated); got != tc.want {
			t.Errorf("CalibrationFactor(%d,%d) = %d, want %d", tc.measured, tc.estimated, got, tc.want)
		}
	}
}

func TestCalibratedEstimate(t *testing.T) {
	if got := agent.CalibratedEstimate(60000, 4); got != 240000 {
		t.Fatalf("CalibratedEstimate = %d, want 240000", got)
	}
	if got := agent.CalibratedEstimate(60000, 1); got != 60000 {
		t.Fatalf("factor=1 must be no-op, got %d", got)
	}
}

func TestReservedCeilingAndShrink(t *testing.T) {
	// window=200k → reserve=max(30k,16k)=30k → ceiling=170k
	if got := agent.ReservedCeiling(200000); got != 170000 {
		t.Fatalf("ReservedCeiling(200000) = %d, want 170000", got)
	}
	// window=100k → reserve=max(15k,16k)=16k → ceiling=84k
	if got := agent.ReservedCeiling(100000); got != 84000 {
		t.Fatalf("ReservedCeiling(100000) = %d, want 84000", got)
	}
	if got := agent.ShrinkWindow(100000, 0); got != 85000 {
		t.Fatalf("ShrinkWindow step0 = %d, want 85000", got)
	}
	if got := agent.ShrinkWindow(100000, 3); got != 35000 {
		t.Fatalf("ShrinkWindow step3 = %d, want 35000", got)
	}
	if got := agent.ShrinkWindow(100000, 99); got != 100000 {
		t.Fatalf("out-of-range step must be no-op, got %d", got)
	}
}

func TestCompactionDecisionCalibrated(t *testing.T) {
	const window, configured, percent = 128000, 100000, 0
	baselineThreshold, baselineCompact := compactionDecision(60000, window, configured, percent)
	if baselineCompact {
		t.Fatal("baseline 60k estimate must not trigger compaction")
	}

	// 实测 120k / 估算 60k → 系数 2 → 校准估算 120k → 膨胀后超阈值，触发压缩。
	threshold, compact := compactionDecisionCalibrated(60000, window, configured, percent, 120000)
	if !compact {
		t.Fatal("measured 120k with estimate 60k must trigger compaction")
	}
	if threshold != baselineThreshold {
		t.Fatalf("threshold changed by calibration: %d vs %d", threshold, baselineThreshold)
	}

	// 无锚点 → 与基线决策逐字节一致。
	th2, c2 := compactionDecisionCalibrated(60000, window, configured, percent, 0)
	th1, c1 := compactionDecision(60000, window, configured, percent)
	if th1 != th2 || c1 != c2 {
		t.Fatalf("no measured anchor must match baseline: (%d,%v) vs (%d,%v)", th1, c1, th2, c2)
	}
}

func TestContextCalibrationEnabledFromEnv(t *testing.T) {
	t.Setenv(contextCalibrationEnv, "1")
	if !contextCalibrationEnabled() {
		t.Fatal("=1 must enable")
	}
	t.Setenv(contextCalibrationEnv, "0")
	if contextCalibrationEnabled() {
		t.Fatal("=0 must disable")
	}
}

func TestCalibratedThresholdReserve(t *testing.T) {
	// 固定阈值小于 ceiling 时不改变。
	if got := calibratedThreshold(50000, 200000); got != 50000 {
		t.Fatalf("small threshold changed: %d", got)
	}
	// 固定阈值大于 ceiling 时收紧到 ceiling。
	if got := calibratedThreshold(200000, 200000); got != 170000 {
		t.Fatalf("large threshold not capped: %d", got)
	}
}
