package app

import (
	"testing"

	"example.com/argentgo/internal/config"
)

func TestCompactionDecisionUsesPercentAndInflation(t *testing.T) {
	// contextWindow=100000、percent=50 -> floor(100000*50/100)=50000 减去
	// 5000 token 的预留（窗口的 5%，低于 8000 上限）-> 45000。
	const window = 100_000
	const percent = 50
	const configured = 5_000 // 必须输给百分比

	cases := []struct {
		name     string
		estimate int
		want     int
		compact  bool
	}{
		{"below inflated threshold", 34_000, 45_000, false}, // 44200 < 45000
		{"at inflated threshold", 35_000, 45_000, true},     // 45500 >= 45000
		{"well above", 90_000, 45_000, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			threshold, compact := compactionDecision(tc.estimate, window, configured, percent)
			if threshold != tc.want {
				t.Fatalf("threshold = %d, want %d", threshold, tc.want)
			}
			if compact != tc.compact {
				t.Fatalf("compact = %v, want %v", compact, tc.compact)
			}
		})
	}
}

func TestCompactionDecisionInheritsConfiguredThreshold(t *testing.T) {
	// Percent <= 0 继承配置的 token 阈值；在比较之前仍会对估算值
	// 应用膨胀系数。
	const configured = 5_000
	for _, percent := range []int{0, -1} {
		if _, compact := compactionDecision(3_800, 100_000, configured, percent); compact {
			t.Fatalf("percent=%d: estimate 3800 (inflated 4940) must not compact", percent)
		}
		threshold, compact := compactionDecision(4_000, 100_000, configured, percent)
		if threshold != configured || !compact {
			t.Fatalf("percent=%d: threshold=%d compact=%v, want %d/true", percent, threshold, compact, configured)
		}
	}
}

func TestCompactionInputs(t *testing.T) {
	cfg := config.Config{
		ContextWindow:              100_000,
		AutoCondenseContextPercent: 50,
		Profiles: map[string]map[string]any{
			"fast":      {"autoCondenseContextPercent": 70},
			"inherited": {"autoCondenseContextPercent": -1},
		},
	}

	// 具名 profile 会覆盖解码出的字段。
	window, configured, percent := compactionInputs(Resolved{
		Cfg:               cfg,
		Profile:           "fast",
		AutoCompactTokens: 9_000,
	})
	if window != 100_000 || configured != 9_000 || percent != 70 {
		t.Fatalf("inputs = %d/%d/%d, want 100000/9000/70", window, configured, percent)
	}

	// -1 被保留，使 CompactionThreshold 继承全局阈值。
	_, _, percent = compactionInputs(Resolved{Cfg: cfg, Profile: "inherited"})
	if percent != -1 {
		t.Fatalf("inherited percent = %d, want -1", percent)
	}

	// 不知道上下文窗口时百分比会被忽略，回退到
	// 配置的 token 阈值。
	_, _, percent = compactionInputs(Resolved{
		Cfg:               config.Config{AutoCondenseContextPercent: 50},
		AutoCompactTokens: 9_000,
	})
	if percent != 0 {
		t.Fatalf("percent without a context window = %d, want 0", percent)
	}
}

func TestSessionCompactorThresholdUsesContextWindow(t *testing.T) {
	res := Resolved{
		Cfg: config.Config{
			ContextWindow:              100_000,
			AutoCondenseContextPercent: 50,
		},
		AutoCompactTokens: 5_000,
	}
	if got := newSessionCompactor(res, nil).CompactThreshold(); got != 45_000 {
		t.Fatalf("context-aware threshold = %d, want 45000", got)
	}

	// 负的配置阈值仍会完全禁用压缩，
	// 即使存在百分比也是如此。
	res.AutoCompactTokens = -1
	if got := newSessionCompactor(res, nil).CompactThreshold(); got != 0 {
		t.Fatalf("disabled threshold = %d, want 0", got)
	}
}
