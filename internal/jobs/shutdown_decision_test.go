package jobs

import (
	"math"
	"testing"
)

// TestShutdownDecisionDecisionTable 覆盖退出决策表的全部情形。
func TestShutdownDecisionDecisionTable(t *testing.T) {
	tests := []struct {
		name         string
		signalCount  int
		graceElapsed bool
		want         string
	}{
		{"无信号且宽限未到", 0, false, "none"},
		{"无信号且宽限已到", 0, true, "none"},
		{"首信号且宽限未到", 1, false, "graceful"},
		{"首信号且宽限已到", 1, true, "force"},
		{"二次信号且宽限未到", 2, false, "force"},
		{"二次信号且宽限已到", 2, true, "force"},
		{"三次信号且宽限未到", 3, false, "force"},
		{"三次信号且宽限已到", 3, true, "force"},
		{"负信号计数按无信号处理", -1, false, "none"},
		{"负信号计数且宽限已到按无信号处理", -7, true, "none"},
		{"极大信号计数且宽限未到", math.MaxInt, false, "force"},
		{"极大信号计数且宽限已到", math.MaxInt, true, "force"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shutdownDecision(tt.signalCount, tt.graceElapsed); got != tt.want {
				t.Fatalf("shutdownDecision(%d, %v) = %q, want %q", tt.signalCount, tt.graceElapsed, got, tt.want)
			}
		})
	}
}

// TestShutdownDecisionBoundary 验证状态切换的关键边界：0/-1 与 1/2 之间，
// 以及极大整数不会溢出为其它决策。
func TestShutdownDecisionBoundary(t *testing.T) {
	boundary := []struct {
		signalCount  int
		graceElapsed bool
		want         string
	}{
		{-1, false, "none"},
		{0, false, "none"},
		{0, true, "none"},
		{1, false, "graceful"},
		{1, true, "force"},
		{2, false, "force"},
		{2, true, "force"},
		{math.MaxInt, false, "force"},
		{math.MinInt, false, "none"},
	}
	for _, tt := range boundary {
		if got := shutdownDecision(tt.signalCount, tt.graceElapsed); got != tt.want {
			t.Fatalf("边界 shutdownDecision(%d, %v) = %q, want %q", tt.signalCount, tt.graceElapsed, got, tt.want)
		}
	}
}

// TestShutdownDecisionStable 验证纯函数语义：同一入参重复调用结果始终一致。
func TestShutdownDecisionStable(t *testing.T) {
	inputs := []struct {
		signalCount  int
		graceElapsed bool
	}{
		{0, false},
		{0, true},
		{1, false},
		{1, true},
		{2, false},
		{2, true},
		{9, true},
	}
	for _, in := range inputs {
		first := shutdownDecision(in.signalCount, in.graceElapsed)
		for i := 0; i < 5; i++ {
			if got := shutdownDecision(in.signalCount, in.graceElapsed); got != first {
				t.Fatalf("第 %d 次调用 shutdownDecision(%d, %v) = %q, want %q", i+1, in.signalCount, in.graceElapsed, got, first)
			}
		}
	}
}

// TestShutdownDecisionCJKAgnostic 验证决策与字符编码无关：入参仅为整数与布尔，
// 返回值只会是预定义的 ASCII 常量之一，不含任何 CJK 字符。
func TestShutdownDecisionCJKAgnostic(t *testing.T) {
	allowed := map[string]bool{shutdownNone: true, shutdownGraceful: true, shutdownForce: true}
	inputs := []struct {
		signalCount  int
		graceElapsed bool
	}{
		{0, false},
		{1, false},
		{1, true},
		{2, false},
		{3, true},
		{-1, true},
	}
	for _, in := range inputs {
		got := shutdownDecision(in.signalCount, in.graceElapsed)
		if !allowed[got] {
			t.Fatalf("shutdownDecision(%d, %v) = %q, 不是预期的 ASCII 决策值", in.signalCount, in.graceElapsed, got)
		}
		for i := 0; i < len(got); i++ {
			if got[i] >= 0x80 {
				t.Fatalf("shutdownDecision(%d, %v) = %q, 含非 ASCII 字节", in.signalCount, in.graceElapsed, got)
			}
		}
	}
}
